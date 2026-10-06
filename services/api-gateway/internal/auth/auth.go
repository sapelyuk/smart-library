// Package auth provides the authentication and authorization middleware of the
// API gateway.
//
// Every request that is not explicitly public has to carry a bearer token in the
// Authorization header. The token is verified once, at the edge, and the identity
// it resolves to is put on the request context, so the handlers and the role
// checks downstream read it instead of parsing the token again.
//
// The roles are the ones user-service issues:
//   - READER: reads the catalog, borrows and returns copies
//   - LIBRARIAN: manages the catalog and the accounts of readers
//   - ADMIN: full access
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Role is the privilege level of an authenticated caller.
type Role string

const (
	RoleReader    Role = "READER"
	RoleLibrarian Role = "LIBRARIAN"
	RoleAdmin     Role = "ADMIN"
)

// Claims is the set of assertions the gateway trusts once a token verifies.
type Claims struct {
	UserID string `json:"sub"`
	Role   string `json:"role"`
	Iss    string `json:"iss"`
	Iat    int64  `json:"iat"`
	Exp    int64  `json:"exp"`
}

// Valid reports whether the token is still within its lifetime.
func (c *Claims) Valid() error {
	if c.Exp == 0 {
		return fmt.Errorf("auth: token has no expiration")
	}

	if time.Now().Unix() > c.Exp {
		return fmt.Errorf("auth: token expired")
	}

	return nil
}

// contextKey is a private type for the context keys of this package, so a key
// defined elsewhere can never collide with one of ours.
type contextKey string

const claimsKey contextKey = "gateway-claims"

// ClaimsFromContext returns the caller resolved by the middleware, if any.
func ClaimsFromContext(ctx context.Context) (*Claims, bool) {
	c, ok := ctx.Value(claimsKey).(*Claims)

	return c, ok
}

// Authenticator rejects a request whose bearer token is missing, malformed, badly
// signed or expired, and otherwise puts the resolved Claims on the context of the
// request it forwards.
type Authenticator struct {
	publicPaths map[string]bool
}

// NewAuthenticator builds an Authenticator for the given public paths. Paths are
// matched without the HTTP method, for example "/v1/auth/login".
func NewAuthenticator(publicPaths ...string) *Authenticator {
	m := make(map[string]bool, len(publicPaths))
	for _, p := range publicPaths {
		m[p] = true
	}
	// The documentation shell has to stay reachable without a session.
	m["/swagger/"] = true

	return &Authenticator{publicPaths: m}
}

// Middleware wraps next with the token check.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Public paths are forwarded untouched.
		path := strings.TrimRight(r.URL.Path, "/")
		if a.publicPaths[path] || a.publicPaths[r.URL.Path] {
			next.ServeHTTP(w, r)

			return
		}

		// Everything else has to present a bearer credential.
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, `{"code":16,"message":"missing authorization header"}`, http.StatusUnauthorized)

			return
		}

		const prefix = "Bearer "

		if !strings.HasPrefix(authHeader, prefix) {
			http.Error(w, `{"code":16,"message":"invalid authorization header format"}`, http.StatusUnauthorized)

			return
		}

		tokenStr := strings.TrimPrefix(authHeader, prefix)

		claims, err := ParseToken(tokenStr)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"code":16,"message":"invalid token: %s"}`, err), http.StatusUnauthorized)

			return
		}

		// Carry the resolved caller into the handler.
		ctx := context.WithValue(r.Context(), claimsKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireRole returns a middleware that only lets callers with one of the listed
// roles through. It has to be mounted behind an Authenticator: with no claims to
// check it answers 401 rather than 403, so a missing session is not reported as a
// permission problem.
func RequireRole(allowed ...Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := ClaimsFromContext(r.Context())
			if !ok {
				http.Error(w, `{"code":16,"message":"unauthenticated"}`, http.StatusUnauthorized)

				return
			}

			role := Role(claims.Role)
			for _, a := range allowed {
				if role == a {
					next.ServeHTTP(w, r)

					return
				}
			}

			http.Error(w, `{"code":7,"message":"insufficient permissions"}`, http.StatusForbidden)
		})
	}
}

// ParseToken verifies the signature of a token and decodes its claims.
//
// The signature is HMAC-SHA256 over "header.payload" with the shared secret from
// the environment, which is the same secret the issuer of the token signs with.
func ParseToken(tokenStr string) (*Claims, error) {
	secret := getSecret()

	parts := strings.Split(tokenStr, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid token format")
	}

	// Reject a foreign or tampered token before looking at what it claims.
	signingInput := parts[0] + "." + parts[1]
	if !verifyHMAC(signingInput, parts[2], secret) {
		return nil, fmt.Errorf("invalid token signature")
	}

	payload, err := base64URLDecode(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid token payload: %w", err)
	}

	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("invalid token claims: %w", err)
	}

	if err := claims.Valid(); err != nil {
		return nil, err
	}

	return &claims, nil
}

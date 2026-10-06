// Package auth provides the authentication and authorization middleware of the
// API gateway.
//
// The gateway does not validate tokens locally. It delegates to user-service
// over gRPC (AuthenticateToken) and the identity the service returns becomes
// the Principal carried in the request context.
//
// The roles are the ones user-service issues:
//
//	READER  — reads the catalog, borrows and returns copies
//	LIBRARIAN — manages the catalog and the accounts of readers
package auth

import (
	"context"
	"net/http"
	"strings"
)

// Principal is the caller the gateway resolves from a bearer token.
type Principal struct {
	UserID string
	Email  string
	Role   string
}

type contextKey int

const principalKey contextKey = iota

// Verifier resolves a bearer token into the principal behind it.
//
// The gateway uses a gRPC implementation that calls AuthenticateToken on
// user-service; tests use a stub.
type Verifier interface {
	Verify(ctx context.Context, token string) (Principal, error)
}

// PrincipalFrom reads the caller out of the request context.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey).(Principal)

	return p, ok
}

// WithPrincipal returns a context carrying the caller.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

// bearerPrefix is the scheme the gateway expects in the Authorization header.
const bearerPrefix = "Bearer "

// tokenFromRequest extracts the bearer token from the request headers.
func tokenFromRequest(r *http.Request) (string, bool) {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(value, bearerPrefix) {
		return "", false
	}

	token := strings.TrimSpace(strings.TrimPrefix(value, bearerPrefix))

	return token, token != ""
}

// Authenticator is the middleware that protects every route behind it.
//
// A missing or malformed header is answered here, before any upstream call; a
// token the verifier rejects becomes 401.
func Authenticator(verifier Verifier, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := tokenFromRequest(r)
		if !ok {
			http.Error(w, `{"error":"missing bearer token"}`, http.StatusUnauthorized)

			return
		}

		principal, err := verifier.Verify(r.Context(), token)
		if err != nil {
			http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)

			return
		}

		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
	})
}

// RequireRole returns a middleware that lets only the named role through.
//
// It answers 403 for an authenticated caller of the wrong role and 401 when no
// caller is in the context at all (i.e. RequireRole is mounted outside
// Authenticator — a configuration error).
func RequireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFrom(r.Context())
			if !ok {
				http.Error(w, `{"error":"authentication required"}`, http.StatusUnauthorized)

				return
			}

			if !strings.EqualFold(principal.Role, role) {
				http.Error(w, `{"error":"insufficient permissions"}`, http.StatusForbidden)

				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

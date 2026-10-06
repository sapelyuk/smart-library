// Package auth предоставляет middleware аутентификации и авторизации для API Gateway.
//
// Gateway проверяет JWT-токен из заголовка Authorization и извлекает user_id и role.
// Для публичных эндпоинтов (login, register) проверка пропускается.
//
// Авторизация на уровне ролей выполняется через проверку роли из токена:
//   - admin: полный доступ (ADMIN в user-service)
//   - librarian: управление каталогом и пользователями (LIBRARIAN в user-service)
//   - reader: чтение каталога, создание borrow/return (READER в user-service)
package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Role — роль пользователя в системе.
type Role string

const (
	RoleReader    Role = "READER"
	RoleLibrarian Role = "LIBRARIAN"
	RoleAdmin     Role = "ADMIN"
)

// Claims — JWT claims для токенов, выдаваемых user-service.
type Claims struct {
	UserID string `json:"sub"`
	Role   string `json:"role"`
	Iss    string `json:"iss"`
	Iat    int64  `json:"iat"`
	Exp    int64  `json:"exp"`
}

// Valid проверяет срок действия токена.
func (c *Claims) Valid() error {
	if c.Exp == 0 {
		return fmt.Errorf("auth: token has no expiration")
	}

	if time.Now().Unix() > c.Exp {
		return fmt.Errorf("auth: token expired")
	}

	return nil
}

// contextKey — тип для ключей в context, чтобы избежать коллизий.
type contextKey string

const claimsKey contextKey = "gateway-claims"

// ClaimsFromContext возвращает Claims из контекста запроса.
func ClaimsFromContext(ctx context.Context) (*Claims, bool) {
	c, ok := ctx.Value(claimsKey).(*Claims)

	return c, ok
}

// Authenticator — middleware, который проверяет JWT-токен и добавляет Claims в контекст.
//
// Для публичных путей (login, register) проверка пропускается.
// Для остальных путей требуется валидный Bearer-токен.
type Authenticator struct {
	publicPaths map[string]bool
}

// NewAuthenticator создаёт Authenticator со списком публичных путей.
// Пути указываются без метода HTTP, только path (например, "/v1/auth/login").
func NewAuthenticator(publicPaths ...string) *Authenticator {
	m := make(map[string]bool, len(publicPaths))
	for _, p := range publicPaths {
		m[p] = true
	}
	// Swagger UI доступен без авторизации.
	m["/swagger/"] = true

	return &Authenticator{publicPaths: m}
}

// Middleware возвращает http.Handler с проверкой JWT.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Пропускаем публичные пути.
		path := strings.TrimRight(r.URL.Path, "/")
		if a.publicPaths[path] || a.publicPaths[r.URL.Path] {
			next.ServeHTTP(w, r)

			return
		}

		// Извлекаем Bearer-токен.
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

		// Добавляем Claims в контекст.
		ctx := context.WithValue(r.Context(), claimsKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireRole — middleware, требующий определённую роль.
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

// GenerateKeyPair создаёт новую пару ключей Ed25519.
// Используется при тестировании или первичной генерации ключей.
func GenerateKeyPair() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

// ParseToken разбирает и валидирует JWT-токен.
// В MVP используется HMAC-SHA256 с общим секретом (SHARE_SECRET из ENV).
func ParseToken(tokenStr string) (*Claims, error) {
	secret := getSecret()

	parts := strings.Split(tokenStr, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid token format")
	}

	// Проверяем подпись HMAC.
	signingInput := parts[0] + "." + parts[1]
	if !verifyHMAC(signingInput, parts[2], secret) {
		return nil, fmt.Errorf("invalid token signature")
	}

	// Декодируем payload.
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

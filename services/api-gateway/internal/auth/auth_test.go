package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sapelyuk/smart-library/services/api-gateway/internal/auth"
)

// stubVerifier returns a fixed principal or error; it implements auth.Verifier.
type stubVerifier struct {
	principal auth.Principal
	err       error
}

func (v *stubVerifier) Verify(_ context.Context, _ string) (auth.Principal, error) {
	return v.principal, v.err
}

func TestAuthenticator_ValidToken(t *testing.T) {
	t.Parallel()

	verifier := &stubVerifier{principal: auth.Principal{UserID: "user-1", Role: "ROLE_READER"}}

	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := auth.PrincipalFrom(r.Context())
		if !ok {
			t.Error("expected principal in context")
		}

		if principal.UserID != "user-1" {
			t.Errorf("expected user-1, got %s", principal.UserID)
		}

		w.WriteHeader(http.StatusOK)
	})

	handler := auth.Authenticator(verifier, backend)

	req := httptest.NewRequest(http.MethodGet, "/v1/books", http.NoBody)
	req.Header.Set("Authorization", "Bearer test-token")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestAuthenticator_MissingToken(t *testing.T) {
	t.Parallel()

	verifier := &stubVerifier{}

	handler := auth.Authenticator(verifier, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("backend should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/books", http.NoBody)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestAuthenticator_InvalidToken(t *testing.T) {
	t.Parallel()

	verifier := &stubVerifier{err: context.DeadlineExceeded}

	handler := auth.Authenticator(verifier, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("backend should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/books", http.NoBody)
	req.Header.Set("Authorization", "Bearer bad-token")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestRequireRole_Librarian(t *testing.T) {
	t.Parallel()

	principal := auth.Principal{UserID: "librarian-1", Role: "ROLE_LIBRARIAN"}
	ctx := auth.WithPrincipal(context.Background(), principal)

	backend := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := auth.RequireRole("ROLE_LIBRARIAN")(backend)

	req := httptest.NewRequest(http.MethodGet, "/v1/users", http.NoBody).WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestRequireRole_ReaderForbidden(t *testing.T) {
	t.Parallel()

	principal := auth.Principal{UserID: "reader-1", Role: "ROLE_READER"}
	ctx := auth.WithPrincipal(context.Background(), principal)

	handler := auth.RequireRole("ROLE_LIBRARIAN")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("backend should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/users", http.NoBody).WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec.Code)
	}
}

func TestRequireRole_NoPrincipal(t *testing.T) {
	t.Parallel()

	handler := auth.RequireRole("ROLE_LIBRARIAN")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("backend should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/users", http.NoBody)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sapelyuk/smart-library/services/user-service/internal/cache"
	"github.com/sapelyuk/smart-library/services/user-service/internal/domain"
	"github.com/sapelyuk/smart-library/services/user-service/internal/repository"
	"github.com/sapelyuk/smart-library/services/user-service/internal/security"
	"github.com/sapelyuk/smart-library/services/user-service/internal/service"
)

// --- mock repositories ---

type mockUserRepo struct {
	user *domain.User
	err  error
}

func (m *mockUserRepo) Create(ctx context.Context, user *domain.User) error { return nil }
func (m *mockUserRepo) Update(ctx context.Context, user *domain.User) error { return nil }

func (m *mockUserRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.user == nil {
		return nil, domain.ErrUserNotFound
	}
	u := *m.user

	return &u, nil
}

func (m *mockUserRepo) GetByEmail(ctx context.Context, email domain.Email) (*domain.User, error) {
	return m.GetByID(ctx, uuid.Nil)
}

func (m *mockUserRepo) List(ctx context.Context, filter domain.UserFilter) ([]*domain.User, int, error) {
	return nil, 0, nil
}

type mockSessionRepo struct {
	session *domain.Session
	err     error
}

func (m *mockSessionRepo) Create(ctx context.Context, session *domain.Session) error     { return nil }
func (m *mockSessionRepo) DeleteByTokenHash(ctx context.Context, tokenHash string) error { return nil }
func (m *mockSessionRepo) DeleteByUser(ctx context.Context, userID uuid.UUID) error      { return nil }
func (m *mockSessionRepo) DeleteByUserExcept(ctx context.Context, userID, except uuid.UUID) error {
	return nil
}

func (m *mockSessionRepo) GetByTokenHash(ctx context.Context, tokenHash string) (*domain.Session, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.session == nil {
		return nil, domain.ErrSessionNotFound
	}
	s := *m.session

	return &s, nil
}

type mockPublisher struct{}

func (m *mockPublisher) Publish(ctx context.Context, eventType string, payload any) error { return nil }
func (m *mockPublisher) Close() error                                                     { return nil }

// --- helpers ---

func newTestService(t *testing.T, users repository.UserRepository, sessions repository.SessionRepository, cacheCfg cache.Config) *service.Service {
	t.Helper()

	svc, err := service.New(users, sessions, &mockPublisher{}, service.Config{
		SessionTTL:     24 * time.Hour,
		PasswordPolicy: domain.DefaultPasswordPolicy(),
		HashParameters: security.DefaultParameters,
		Now:            time.Now,
		SessionCache:   cacheCfg,
	})
	if err != nil {
		t.Fatalf("build service: %v", err)
	}

	return svc
}

func makeSession(userID uuid.UUID, tokenHash string, ttl time.Duration) *domain.Session {
	now := time.Now().UTC()

	return domain.NewSession(userID, tokenHash, ttl, now)
}

func makeUser(userID uuid.UUID, role domain.Role, status domain.UserStatus) *domain.User {
	if status == "" {
		status = domain.UserStatusActive
	}

	return &domain.User{
		ID:     userID,
		Email:  "test@library.local",
		Role:   role,
		Status: status,
	}
}

func makePrincipal(userID uuid.UUID, role domain.Role) domain.Principal {
	return domain.Principal{
		UserID:    userID,
		SessionID: uuid.New(),
		Email:     "test@library.local",
		Role:      role,
	}
}

// --- tests ---

func TestAuthenticateSession_CacheHit(t *testing.T) {
	userID := uuid.New()
	tokenHash := "test-token-hash"

	users := &mockUserRepo{
		user: makeUser(userID, domain.RoleReader, ""),
	}

	sessions := &mockSessionRepo{
		session: makeSession(userID, tokenHash, 24*time.Hour),
	}

	cacheCfg := cache.Config{
		Enabled: true,
		TTL:     60 * time.Second,
		MissTTL: 5 * time.Second,
		MaxCost: 1000,
	}

	svc := newTestService(t, users, sessions, cacheCfg)

	// First call — cache miss, hits DB and populates the cache.
	principal1, _, err := svc.AuthenticateSession(context.Background(), "raw-token")
	if err != nil {
		t.Fatalf("first authenticate: %v", err)
	}

	if principal1.UserID != userID {
		t.Errorf("principal.UserID = %s, want %s", principal1.UserID, userID)
	}

	// Second call — should hit the cache. Replace the session repo with one
	// that would fail if called; if the cache works, it won't be called.
	sessions.err = errors.New("session repo should not be called on cache hit")

	principal2, sess2, err := svc.AuthenticateSession(context.Background(), "raw-token")
	if err != nil {
		t.Fatalf("second authenticate (cache hit) failed: %v", err)
	}

	if principal2.UserID != userID {
		t.Errorf("principal.UserID = %s, want %s", principal2.UserID, userID)
	}

	if sess2 == nil {
		t.Error("expected session from cache hit")
	}
}

func TestAuthenticateSession_Success(t *testing.T) {
	userID := uuid.New()
	tokenHash := "test-token-hash"

	users := &mockUserRepo{
		user: makeUser(userID, domain.RoleReader, ""),
	}

	sessions := &mockSessionRepo{
		session: makeSession(userID, tokenHash, 24*time.Hour),
	}

	cacheCfg := cache.Config{
		Enabled: true,
		TTL:     60 * time.Second,
		MissTTL: 5 * time.Second,
		MaxCost: 1000,
	}

	svc := newTestService(t, users, sessions, cacheCfg)

	principal, sess, err := svc.AuthenticateSession(context.Background(), "raw-token")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}

	if principal.UserID != userID {
		t.Errorf("principal.UserID = %s, want %s", principal.UserID, userID)
	}

	if sess == nil {
		t.Fatal("session is nil")
	}

	if sess.UserID != userID {
		t.Errorf("session.UserID = %s, want %s", sess.UserID, userID)
	}
}

func TestAuthenticateSession_SessionNotFound_CachesMiss(t *testing.T) {
	users := &mockUserRepo{}
	sessions := &mockSessionRepo{
		err: domain.ErrSessionNotFound,
	}

	cacheCfg := cache.Config{
		Enabled: true,
		TTL:     60 * time.Second,
		MissTTL: 2 * time.Second,
		MaxCost: 1000,
	}

	svc := newTestService(t, users, sessions, cacheCfg)

	// First call — session not found, should be cached as a miss.
	_, _, err := svc.AuthenticateSession(context.Background(), "unknown-token")
	if err == nil || !errors.Is(err, domain.ErrUnauthenticated) {
		t.Errorf("expected ErrUnauthenticated, got %v", err)
	}

	// Second call — should hit the miss cache and return ErrUnauthenticated
	// without calling the repo again.
	sessions.err = errors.New("session repo should not be called on miss cache hit")

	_, _, err = svc.AuthenticateSession(context.Background(), "unknown-token")
	if err == nil || !errors.Is(err, domain.ErrUnauthenticated) {
		t.Errorf("expected ErrUnauthenticated from miss cache, got %v", err)
	}
}

func TestLogout_InvalidatesCache(t *testing.T) {
	userID := uuid.New()
	tokenHash := "test-token-hash"

	users := &mockUserRepo{
		user: makeUser(userID, domain.RoleReader, ""),
	}

	sessions := &mockSessionRepo{
		session: makeSession(userID, tokenHash, 24*time.Hour),
	}

	cacheCfg := cache.Config{
		Enabled: true,
		TTL:     60 * time.Second,
		MissTTL: 5 * time.Second,
		MaxCost: 1000,
	}

	svc := newTestService(t, users, sessions, cacheCfg)

	// Authenticate to populate cache.
	_, _, err := svc.AuthenticateSession(context.Background(), "raw-token")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}

	// Logout.
	caller := makePrincipal(userID, domain.RoleReader)
	caller.SessionID = sessions.session.ID

	err = svc.Logout(context.Background(), caller)
	if err != nil {
		t.Fatalf("logout: %v", err)
	}

	// Verify cache is invalidated — next authenticate should hit DB.
	sessions.session = nil
	sessions.err = domain.ErrSessionNotFound

	// The cache should be empty now.
	_, _, err = svc.AuthenticateSession(context.Background(), "raw-token")
	if err == nil {
		t.Error("expected error after cache invalidation and session deletion")
	}
}

func TestAuthenticateSession_DeactivatedUser(t *testing.T) {
	userID := uuid.New()
	tokenHash := "test-token-hash"

	users := &mockUserRepo{
		user: makeUser(userID, domain.RoleReader, domain.UserStatusDeactivated),
	}

	sessions := &mockSessionRepo{
		session: makeSession(userID, tokenHash, 24*time.Hour),
	}

	cacheCfg := cache.Config{
		Enabled: true,
		TTL:     60 * time.Second,
		MissTTL: 5 * time.Second,
		MaxCost: 1000,
	}

	svc := newTestService(t, users, sessions, cacheCfg)

	_, _, err := svc.AuthenticateSession(context.Background(), "raw-token")
	if err == nil || !errors.Is(err, domain.ErrDeactivated) {
		t.Errorf("expected ErrDeactivated, got %v", err)
	}
}

func TestAuthenticateSession_ExpiredSession(t *testing.T) {
	userID := uuid.New()
	tokenHash := "test-token-hash"

	users := &mockUserRepo{
		user: makeUser(userID, domain.RoleReader, ""),
	}

	sessions := &mockSessionRepo{
		session: makeSession(userID, tokenHash, -1*time.Hour), // expired
	}

	cacheCfg := cache.Config{
		Enabled: true,
		TTL:     60 * time.Second,
		MissTTL: 5 * time.Second,
		MaxCost: 1000,
	}

	svc := newTestService(t, users, sessions, cacheCfg)

	_, _, err := svc.AuthenticateSession(context.Background(), "raw-token")
	if err == nil || !errors.Is(err, domain.ErrSessionExpired) {
		t.Errorf("expected ErrSessionExpired, got %v", err)
	}
}

func TestAuthenticateSession_EmptyToken(t *testing.T) {
	users := &mockUserRepo{}
	sessions := &mockSessionRepo{}

	cacheCfg := cache.Config{
		Enabled: true,
		TTL:     60 * time.Second,
		MissTTL: 5 * time.Second,
		MaxCost: 1000,
	}

	svc := newTestService(t, users, sessions, cacheCfg)

	_, _, err := svc.AuthenticateSession(context.Background(), "")
	if err == nil || !errors.Is(err, domain.ErrUnauthenticated) {
		t.Errorf("expected ErrUnauthenticated, got %v", err)
	}
}

func TestAuthenticateSession_CacheDisabled(t *testing.T) {
	userID := uuid.New()
	tokenHash := "test-token-hash"

	users := &mockUserRepo{
		user: makeUser(userID, domain.RoleReader, ""),
	}

	sessions := &mockSessionRepo{
		session: makeSession(userID, tokenHash, 24*time.Hour),
	}

	cacheCfg := cache.Config{
		Enabled: false,
	}

	svc := newTestService(t, users, sessions, cacheCfg)

	principal, _, err := svc.AuthenticateSession(context.Background(), "raw-token")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}

	if principal.UserID != userID {
		t.Errorf("principal.UserID = %s, want %s", principal.UserID, userID)
	}
}

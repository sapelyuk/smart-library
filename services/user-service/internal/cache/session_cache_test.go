package cache

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sapelyuk/smart-library/services/user-service/internal/domain"
)

func newTestPrincipal() *domain.Principal {
	return &domain.Principal{
		UserID:    uuid.New(),
		SessionID: uuid.New(),
		Email:     "test@library.local",
		Role:      domain.RoleReader,
	}
}

func newTestConfig() Config {
	return Config{
		Enabled: true,
		TTL:     60 * time.Second,
		MissTTL: 5 * time.Second,
		MaxCost: 1000,
	}
}

func TestNewSessionCache_Disabled(t *testing.T) {
	cfg := newTestConfig()
	cfg.Enabled = false

	c := NewSessionCache(cfg)

	// A disabled cache never hits.
	_, _, ok := c.Get("any-token")
	if ok {
		t.Error("disabled cache should always miss")
	}

	// A disabled cache never stores.
	c.Set("token", newTestPrincipal(), nil, time.Now().Add(time.Hour))

	_, _, ok = c.Get("token")
	if ok {
		t.Error("disabled cache should not store anything")
	}
}

func TestSessionCache_GetHit(t *testing.T) {
	cfg := newTestConfig()
	c := NewSessionCache(cfg)
	if c == nil {
		t.Fatal("cache is nil")
	}

	p := newTestPrincipal()
	c.Set("token-hash-1", p, nil, time.Now().Add(time.Hour))

	got, sess, ok := c.Get("token-hash-1")
	if !ok {
		t.Fatal("expected a hit")
	}

	if got.UserID != p.UserID {
		t.Errorf("expected user %s, got %s", p.UserID, got.UserID)
	}

	if sess != nil {
		t.Error("expected nil session for cache entry")
	}
}

func TestSessionCache_GetMiss(t *testing.T) {
	cfg := newTestConfig()
	c := NewSessionCache(cfg)

	_, _, ok := c.Get("unknown-token")
	if ok {
		t.Error("expected a miss for unknown token")
	}
}

func TestSessionCache_GetExpired(t *testing.T) {
	cfg := newTestConfig()
	c := NewSessionCache(cfg)

	p := newTestPrincipal()
	// ristretto drops expired entries automatically, so Get should miss.
	c.Set("token-hash-1", p, nil, time.Now().Add(-1*time.Second))

	_, _, ok := c.Get("token-hash-1")
	if ok {
		t.Error("expected miss for expired entry")
	}
}

func TestSessionCache_SetMissTTL(t *testing.T) {
	cfg := newTestConfig()
	cfg.MissTTL = 2 * time.Second
	c := NewSessionCache(cfg)

	// Store a nil entry (simulates session not found).
	c.Set("unknown-token", nil, nil, time.Now().Add(cfg.MissTTL))

	// Should hit the miss cache. The entry exists but principal is nil.
	principal, session, ok := c.Get("unknown-token")
	if !ok {
		t.Fatal("expected a hit (miss cache) for unknown token")
	}
	if principal != nil {
		t.Error("expected nil principal for miss cache entry")
	}
	if session != nil {
		t.Error("expected nil session for miss cache entry")
	}

	// After miss TTL expires, should return to miss.
	time.Sleep(3 * time.Second)

	_, _, ok = c.Get("unknown-token")
	if ok {
		t.Error("expected a miss after miss TTL expired")
	}
}

func TestSessionCache_InvalidateByUser(t *testing.T) {
	cfg := newTestConfig()
	c := NewSessionCache(cfg)

	p1 := newTestPrincipal()
	p2 := newTestPrincipal()
	p2.UserID = uuid.New() // different user

	c.Set("token-1", p1, nil, time.Now().Add(time.Hour))
	c.Set("token-2", p2, nil, time.Now().Add(time.Hour))

	// Both should be present.
	if _, _, ok := c.Get("token-1"); !ok {
		t.Error("expected token-1 to be present")
	}
	if _, _, ok := c.Get("token-2"); !ok {
		t.Error("expected token-2 to be present")
	}

	// Invalidate by p1's user.
	c.InvalidateByUser(p1.UserID)

	// token-1 should be gone.
	if _, _, ok := c.Get("token-1"); ok {
		t.Error("expected token-1 to be invalidated")
	}

	// token-2 should still be present.
	if _, _, ok := c.Get("token-2"); !ok {
		t.Error("expected token-2 to still be present")
	}
}

func TestSessionCache_InvalidateAll(t *testing.T) {
	cfg := newTestConfig()
	c := NewSessionCache(cfg)

	p := newTestPrincipal()
	c.Set("token-1", p, nil, time.Now().Add(time.Hour))
	c.Set("token-2", p, nil, time.Now().Add(time.Hour))

	c.InvalidateAll()

	if _, _, ok := c.Get("token-1"); ok {
		t.Error("expected token-1 to be cleared")
	}
	if _, _, ok := c.Get("token-2"); ok {
		t.Error("expected token-2 to be cleared")
	}
}

func TestSessionCache_Stats(t *testing.T) {
	cfg := newTestConfig()
	c := NewSessionCache(cfg)

	p := newTestPrincipal()
	c.Set("token-1", p, nil, time.Now().Add(time.Hour))
	c.Set("token-2", p, nil, time.Now().Add(time.Hour))
	c.Get("token-1") // hit
	c.Get("unknown") // miss

	stats := c.Stats()

	if stats.Hits != 1 {
		t.Errorf("expected 1 hit, got %d", stats.Hits)
	}
	if stats.Misses != 1 {
		t.Errorf("expected 1 miss, got %d", stats.Misses)
	}
	if stats.Sets != 2 {
		t.Errorf("expected 2 sets, got %d", stats.Sets)
	}
}

func TestSessionCache_DisabledStats(t *testing.T) {
	cfg := newTestConfig()
	cfg.Enabled = false
	c := NewSessionCache(cfg)

	stats := c.Stats()

	if stats.Hits != 0 || stats.Misses != 0 {
		t.Error("disabled cache stats should be zero")
	}
}

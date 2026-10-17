// Package cache provides an in-memory session cache backed by ristretto.
//
// The cache speeds up the authentication hot path: a successful lookup
// avoids two round trips to PostgreSQL (session by token hash, then user by
// id). Misses are cached with a shorter TTL to protect against a DoS that
// floods the service with unknown tokens.
//
// Invalidation happens on every operation that revokes or changes a session
// (logout, password change, deactivation).
package cache

import (
	"sync"
	"time"

	"github.com/dgraph-io/ristretto"
	"github.com/google/uuid"

	"github.com/sapelyuk/smart-library/services/user-service/internal/domain"
)

// SessionCache is a local in-memory cache for authenticated principals.
type SessionCache struct {
	store       *ristretto.Cache
	missTTL     time.Duration
	userIndex   map[uuid.UUID][]string
	userIndexMu sync.RWMutex
}

// cacheEntry holds the resolved principal, its session and the user id
// needed for reverse-index invalidation.
type cacheEntry struct {
	Principal *domain.Principal
	Session   *domain.Session
	UserID    uuid.UUID
}

// Config carries the knobs for the session cache.
type Config struct {
	// Enabled controls whether the cache is active. When false the cache
	// behaves as a pure no-op and never stores anything.
	Enabled bool

	// TTL is the time-to-live for a successful authentication entry.
	TTL time.Duration

	// MissTTL is the time-to-live when the token is unknown or expired.
	// A shorter TTL prevents a DoS from constantly hammering the database
	// with the same unknown token.
	MissTTL time.Duration

	// MaxCost is the maximum number of entries the cache holds. Each entry
	// costs 1, so this is the maximum number of cached sessions.
	MaxCost int64
}

// NewSessionCache builds a cache from the given configuration.
//
// When cfg.Enabled is false a no-op cache is returned: all reads miss and
// writes are silently dropped. This keeps the rest of the code simple — the
// cache is always injected, the service layer never checks a flag.
func NewSessionCache(cfg Config) *SessionCache {
	if !cfg.Enabled {
		return &SessionCache{
			store:     nil,
			missTTL:   cfg.MissTTL,
			userIndex: make(map[uuid.UUID][]string),
		}
	}

	store, err := ristretto.NewCache(&ristretto.Config{
		NumCounters: cfg.MaxCost * 10,
		MaxCost:     cfg.MaxCost,
		BufferItems: 64,
		Metrics:     true,
	})
	if err != nil {
		// A misconfigured max cost is a programming error; fall back to
		// no-op cache.
		return &SessionCache{
			store:     nil,
			missTTL:   cfg.MissTTL,
			userIndex: make(map[uuid.UUID][]string),
		}
	}

	return &SessionCache{
		store:     store,
		missTTL:   cfg.MissTTL,
		userIndex: make(map[uuid.UUID][]string),
	}
}

// Get looks up a principal by the token hash.
//
// Returns the principal, session and true on a hit, or nil, nil, false on a
// miss.
func (c *SessionCache) Get(tokenHash string) (*domain.Principal, *domain.Session, bool) {
	if c.store == nil {
		return nil, nil, false
	}

	val, ok := c.store.Get(tokenHash)
	if !ok {
		return nil, nil, false
	}

	entry := val.(*cacheEntry)

	return entry.Principal, entry.Session, true
}

// Set stores a resolved principal and its session.
//
// If the principal is nil the cache records a "not found" tombstone with
// missTTL, protecting the database from repeated lookups for the same
// unknown token.
func (c *SessionCache) Set(tokenHash string, p *domain.Principal, session *domain.Session, expiresAt time.Time) {
	if c.store == nil {
		return
	}

	var userID uuid.UUID
	if p != nil {
		userID = p.UserID
	}

	entry := &cacheEntry{
		Principal: p,
		Session:   session,
		UserID:    userID,
	}

	ttl := time.Until(expiresAt)
	if ttl <= 0 {
		// Don't cache entries that are already expired.
		return
	}

	c.store.SetWithTTL(tokenHash, entry, 1, ttl)
	c.store.Wait()

	// Maintain the reverse index for fast invalidation.
	if p != nil {
		c.userIndexMu.Lock()
		c.userIndex[p.UserID] = append(c.userIndex[p.UserID], tokenHash)
		c.userIndexMu.Unlock()
	}
}

// InvalidateByUser removes all cached entries for the given user.
//
// This is called when a session is revoked, the password is changed, or the
// user is deactivated.
func (c *SessionCache) InvalidateByUser(userID uuid.UUID) {
	if c.store == nil {
		return
	}

	c.userIndexMu.Lock()
	tokens, ok := c.userIndex[userID]
	if ok {
		delete(c.userIndex, userID)
	}
	c.userIndexMu.Unlock()

	for _, token := range tokens {
		c.store.Del(token)
	}
}

// InvalidateAll clears every cached entry.
func (c *SessionCache) InvalidateAll() {
	if c.store == nil {
		return
	}

	c.store.Clear()

	c.userIndexMu.Lock()
	c.userIndex = make(map[uuid.UUID][]string)
	c.userIndexMu.Unlock()
}

// Stats returns a snapshot of the cache metrics.
func (c *SessionCache) Stats() *CacheStats {
	if c.store == nil {
		return &CacheStats{}
	}

	m := c.store.Metrics

	return &CacheStats{
		Hits:         int64(m.Hits()),
		Misses:       int64(m.Misses()),
		Sets:         int64(m.KeysAdded() + m.KeysUpdated()),
		Evictions:    int64(m.KeysEvicted()),
		CurrentCost:  int64(m.CostAdded()),
		CurrentCount: int64(m.KeysAdded()),
	}
}

// CacheStats is a snapshot of cache metrics.
type CacheStats struct {
	Hits         int64
	Misses       int64
	Sets         int64
	Evictions    int64
	CurrentCost  int64
	CurrentCount int64
}

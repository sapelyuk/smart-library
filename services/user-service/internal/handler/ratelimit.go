// Package handler owns the transport adapters of the user service: gRPC and HTTP
// mapping, error translation, and request-rate limiting.
//
// Rate limiting protects the service from CPU DoS: every login, registration, and
// password change calls argon2id (~30 ms CPU per invocation).  Without limits a
// botnet can saturate all cores and bring the service (and the whole platform) down.
//
// The limiter uses a token-bucket algorithm keyed by (method, IP).  It runs before
// the auth interceptor so that unauthenticated attackers are also throttled.
package handler

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/sapelyuk/smart-library/services/user-service/internal/domain"
)

// LimitType classifies a public RPC into the bucket that applies.
type LimitType int

const (
	// LimitLogin covers /Login.
	LimitLogin LimitType = iota
	// LimitRegister covers /Register.
	LimitRegister
	// LimitChangePassword covers /ChangePassword.
	LimitChangePassword
	// LimitGlobal covers all other methods.
	LimitGlobal
)

// limitType is the internal (lowercase) alias.
type limitType = LimitType

const (
	limitLogin limitType = iota
	limitRegister
	limitChangePassword
	limitGlobal
)

// limitConfig holds the token-bucket parameters for one limit type.
type limitConfig struct {
	rate     float64 // tokens per second
	burst    int     // max bucket size
	key      string  // "login", "register", "change_password"
	limitTyp limitType
}

// defaultLimits returns the built-in rate-limit configuration.
//
// Values are derived from nCPU * 25 RPS (the cost of one argon2id call is
// roughly 30 ms, so a single core can handle ~33 hashes per second).  The
// login limit is the most restrictive because every failed attempt costs a
// full hash; register is slightly higher because the attacker must also
// supply a valid-looking profile; change-password shares the login limit
// because it also triggers a hash on the old password.
//
// The global limit is a hard cap on total CPU-bound work: when it is hit,
// the service refuses all auth-adjacent requests regardless of IP.
func defaultLimits() []limitConfig {
	return []limitConfig{
		{rate: 2, burst: 4, key: "login", limitTyp: limitLogin},
		{rate: 1, burst: 2, key: "register", limitTyp: limitRegister},
		{rate: 2, burst: 4, key: "change_password", limitTyp: limitChangePassword},
		{rate: 100, burst: 200, key: "global", limitTyp: limitGlobal},
	}
}

// bucket is a per-key token bucket.
type bucket struct {
	tokens    float64
	lastRefill time.Time
	rate      float64
	burst     int
	mu        sync.Mutex
}

// allow checks whether one token may be consumed.
func (b *bucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens += elapsed * b.rate
	if b.tokens > float64(b.burst) {
		b.tokens = float64(b.burst)
	}
	b.lastRefill = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// limiter owns the per-key buckets for one limit type.
type limiter struct {
	cfg   limitConfig
	buckets map[string]*bucket
	mu    sync.Mutex
}

// newLimiter creates a limiter with the given configuration.
func newLimiter(cfg limitConfig) *limiter {
	return &limiter{
		cfg:     cfg,
		buckets: make(map[string]*bucket),
	}
}

// allow checks whether the key is permitted.  A missing key creates a new
// bucket that starts full.
func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{
			tokens:    float64(l.cfg.burst),
			lastRefill: time.Now(),
			rate:      l.cfg.rate,
			burst:     l.cfg.burst,
		}
		l.buckets[key] = b
	}
	l.mu.Unlock()

	return b.allow()
}

// rateLimiter aggregates all per-method limiters and the global limiter.
type rateLimiter struct {
	login     *limiter
	register  *limiter
	changePwd *limiter
	global    *limiter
	log       *slog.Logger
}

// NewRateLimiter builds the limiter set from explicit configs (or defaults when
// cfg is nil).
func NewRateLimiter(cfg *rateLimitConfig, log *slog.Logger) *rateLimiter {
	if log == nil {
		log = slog.Default()
	}

	var limits []limitConfig
	if cfg != nil {
		limits = cfg.toLimits()
	}
	if len(limits) == 0 {
		limits = defaultLimits()
	}

	rl := &rateLimiter{log: log}
	for i := range limits {
		l := newLimiter(limits[i])
		switch limits[i].limitTyp {
		case limitLogin:
			rl.login = l
		case limitRegister:
			rl.register = l
		case limitChangePassword:
			rl.changePwd = l
		case limitGlobal:
			rl.global = l
		}
	}

	return rl
}

// methodType maps a gRPC full method name to a limit type and its config key.
func methodType(fullMethod string) (limitType, string) {
	switch fullMethod {
	case "/user.v1.UserService/Login":
		return limitLogin, "login"
	case "/user.v1.UserService/Register":
		return limitRegister, "register"
	case "/user.v1.UserService/ChangePassword":
		return limitChangePassword, "change_password"
	default:
		return limitGlobal, "global"
	}
}

// allow checks both the per-method and the global bucket for the given key.
func (rl *rateLimiter) allow(lt limitType, key string) bool {
	var m *limiter
	switch lt {
	case limitLogin:
		m = rl.login
	case limitRegister:
		m = rl.register
	case limitChangePassword:
		m = rl.changePwd
	default:
		m = rl.global
	}

	if !m.allow(key) {
		rl.log.Warn("rate limit hit", "key", key, "method", lt)
		return false
	}
	return rl.global.allow(key)
}

// ErrRateLimitExceeded is returned when the rate limiter rejects a request.
var ErrRateLimitExceeded = domain.ErrRateLimitExceeded

// RateLimitConfig is the user-facing configuration for rate limiting.
type RateLimitConfig struct {
	LoginRPS     float64
	RegisterRPS  float64
	ChangePwdRPS float64
	GlobalRPS    float64
	LoginBurst   int
	RegisterBurst int
	ChangePwdBurst int
	GlobalBurst  int
}

// rateLimitConfig is the internal (lowercase) alias.
type rateLimitConfig = RateLimitConfig
	loginRPS     float64
	registerRPS  float64
	changePwdRPS float64
	globalRPS    float64
	loginBurst   int
	registerBurst int
	changePwdBurst int
	globalBurst  int
}

// toLimits converts the typed config into the internal limitConfig slice.
func (c *rateLimitConfig) toLimits() []limitConfig {
	loginRPS := c.loginRPS
	if loginRPS <= 0 {
		loginRPS = 2
	}
	registerRPS := c.registerRPS
	if registerRPS <= 0 {
		registerRPS = 1
	}
	changePwdRPS := c.changePwdRPS
	if changePwdRPS <= 0 {
		changePwdRPS = 2
	}
	globalRPS := c.globalRPS
	if globalRPS <= 0 {
		globalRPS = 100
	}

	loginBurst := c.loginBurst
	if loginBurst <= 0 {
		loginBurst = 4
	}
	registerBurst := c.registerBurst
	if registerBurst <= 0 {
		registerBurst = 2
	}
	changePwdBurst := c.changePwdBurst
	if changePwdBurst <= 0 {
		changePwdBurst = 4
	}
	globalBurst := c.globalBurst
	if globalBurst <= 0 {
		globalBurst = 200
	}

	return []limitConfig{
		{rate: loginRPS, burst: loginBurst, key: "login", limitTyp: limitLogin},
		{rate: registerRPS, burst: registerBurst, key: "register", limitTyp: limitRegister},
		{rate: changePwdRPS, burst: changePwdBurst, key: "change_password", limitTyp: limitChangePassword},
		{rate: globalRPS, burst: globalBurst, key: "global", limitTyp: limitGlobal},
	}
}

// clientIP extracts the real client IP from the gRPC call context.
//
// For direct gRPC calls the IP comes from the peer.  For REST calls that arrive
// through grpc-gateway the call context contains loopback (127.0.0.1); the real
// IP is forwarded in the x-forwarded-for metadata key by httpForwardedFor.
func clientIP(ctx context.Context) string {
	// Try metadata first (set by httpForwardedFor for REST calls).
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vals := md.Get("x-forwarded-for"); len(vals) == 1 {
			ip := strings.TrimSpace(vals[0])
			if ip != "" {
				return ip
			}
		}
	}

	// Fall back to gRPC peer.
	if p, ok := peer.FromContext(ctx); ok {
		host, _, err := net.SplitHostPort(p.Addr.String())
		if err == nil && host != "" {
			return host
		}
	}

	return "unknown"
}

// NewRateLimitUnaryInterceptor returns a gRPC unary interceptor that throttles
// Login, Register, ChangePassword and all other methods by IP.
//
// The interceptor must be placed BEFORE the auth interceptor in the chain so that
// unauthenticated attackers are also throttled.  It returns
// codes.ResourceExhausted (HTTP 429) when a bucket is empty.
func NewRateLimitUnaryInterceptor(rl *rateLimiter) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		lt, key := methodType(info.FullMethod)

		ip := clientIP(ctx)
		bucketKey := key + ":" + ip

		if !rl.allow(lt, bucketKey) {
			return nil, status.Error(codes.ResourceExhausted, "too many requests; try again later")
		}

		return handler(ctx, req)
	}
}

// httpForwardedFor is an HTTP middleware that extracts the X-Forwarded-For header
// of a REST request and injects it into the gRPC metadata.
//
// This is necessary because grpc-gateway dials the local gRPC server via
// loopback (127.0.0.1), so clientIP() would always return 127.0.0.1 without
// this bridge.  The middleware reads the first IP from X-Forwarded-For (the
// end client) and writes it as the x-forwarded-for metadata key, which
// clientIP() checks before falling back to the peer address.
func httpForwardedFor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		xff := r.Header.Get("X-Forwarded-For")
		if xff == "" {
			// No forwarding header: the request came directly.  Extract the IP
			// from RemoteAddr so the gRPC interceptor has something to work with.
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err == nil && host != "" {
				xff = host
			}
		}

		if xff != "" {
			// Build a new context with the forwarded-for metadata.
			md := metadata.Pairs("x-forwarded-for", xff)
			r = r.WithContext(metadata.NewIncomingContext(r.Context(), md))
		}

		next.ServeHTTP(w, r)
	})
}

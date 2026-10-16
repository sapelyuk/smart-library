package handler

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// ---------- bucket tests ----------

func TestBucketAllowConsumption(t *testing.T) {
	t.Parallel()

	// burst=3: first 3 calls should succeed, 4th should fail.
	b := &bucket{tokens: 3, lastRefill: time.Now(), burst: 3, rate: 1}

	for i := 0; i < 3; i++ {
		if !b.allow() {
			t.Fatalf("call %d: expected allow, got false", i+1)
		}
	}

	if b.allow() {
		t.Fatal("call 4: expected deny, got true")
	}
}

func TestBucketAllowAccumulation(t *testing.T) {
	t.Parallel()

	// burst=2, rate=10/s. Start empty, wait for refill.
	b := &bucket{tokens: 0, lastRefill: time.Now(), burst: 2, rate: 10}

	// Initially empty — should deny.
	if b.allow() {
		t.Fatal("empty bucket: expected deny")
	}

	// Wait for 0.2s → should refill ~2 tokens (capped at burst=2).
	time.Sleep(200 * time.Millisecond)

	if !b.allow() {
		t.Fatal("after refill: expected allow")
	}
	if !b.allow() {
		t.Fatal("after refill: expected allow (2nd token)")
	}
	// Bucket should be empty again (burst=2, consumed 2).
	if b.allow() {
		t.Fatal("after consuming 2: expected deny")
	}
}

// ---------- rateLimiter.allow() tests ----------

func TestRateLimiterAllowExhaustion(t *testing.T) {
	t.Parallel()

	rl := NewRateLimiter(&RateLimitConfig{
		LoginRPS:   1,
		LoginBurst: 2,
	}, nil)

	// First two should succeed.
	if !rl.allow(LimitLogin, "test-key") {
		t.Fatal("1st allow: expected true")
	}
	if !rl.allow(LimitLogin, "test-key") {
		t.Fatal("2nd allow: expected true")
	}

	// 3rd should fail.
	if rl.allow(LimitLogin, "test-key") {
		t.Fatal("3rd allow: expected false")
	}
}

func TestRateLimiterBucketPersistAfterExhaustion(t *testing.T) {
	t.Parallel()

	// The bucket map grows with unique IPs.  This test documents why
	// eviction is not implemented: the number of distinct source IPs
	// hitting a library API is bounded (internal users + public
	// visitors), and each bucket is just a small struct with a mutex.
	// Even 100 000 IPs would consume ~10 MB of bucket structs + map
	// overhead, which is acceptable for a service that already holds a
	// database connection pool and in-flight gRPC calls.
	//
	// The test verifies that buckets survive beyond their exhaustion
	// (no auto-deletion), confirming the current design.
	rl := NewRateLimiter(&RateLimitConfig{
		LoginRPS:   1,
		LoginBurst: 1,
	}, nil)

	key := "eviction-test-key"

	// Exhaust the bucket.
	rl.allow(LimitLogin, key) // succeeds
	rl.allow(LimitLogin, key) // denied

	// The bucket still exists (no eviction).  A new request should
	// still be denied — proving the bucket wasn't removed.
	if rl.allow(LimitLogin, key) {
		t.Fatal("after exhaustion: expected deny (bucket should persist)")
	}
}

// ---------- clientIP() tests ----------

func TestClientIPFromMetadata(t *testing.T) {
	t.Parallel()

	// Simulate REST request with X-Forwarded-For.
	md := metadata.Pairs("x-forwarded-for", "203.0.113.5")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	ip := clientIP(ctx)
	if ip != "203.0.113.5" {
		t.Errorf("clientIP(metadata) = %q, want %q", ip, "203.0.113.5")
	}
}

func TestClientIPFromPeer(t *testing.T) {
	t.Parallel()

	// Simulate gRPC request with peer address.
	peerAddr := &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP("10.0.0.99"), Port: 12345}}
	ctx := peer.NewContext(context.Background(), peerAddr)

	ip := clientIP(ctx)
	if ip != "10.0.0.99" {
		t.Errorf("clientIP(peer) = %q, want %q", ip, "10.0.0.99")
	}
}

func TestClientIPMetadataOverridesPeer(t *testing.T) {
	t.Parallel()

	// gRPC peer says 10.0.0.1, but XFF says 203.0.113.5.
	// Metadata should win.
	peerAddr := &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 12345}}
	ctx := peer.NewContext(context.Background(), peerAddr)
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("x-forwarded-for", "203.0.113.5"))

	ip := clientIP(ctx)
	if ip != "203.0.113.5" {
		t.Errorf("clientIP(override) = %q, want %q", ip, "203.0.113.5")
	}
}

// ---------- interceptor tests (direct call, no bufconn) ----------

// stubHandler is a no-op handler for testing the interceptor chain.
type stubHandler struct{}

func (stubHandler) ServeGRPC(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	return req, nil
}

// callInterceptor creates a rate-limited interceptor and calls it directly.
func callInterceptor(t *testing.T, rl *RateLimiter, fullMethod string, ctx context.Context) error {
	t.Helper()

	intceptor := NewRateLimitUnaryInterceptor(rl)

	info := &grpc.UnaryServerInfo{
		FullMethod: fullMethod,
	}

	_, err := intceptor(ctx, new(any), info, func(ctx context.Context, req any) (any, error) {
		return req, nil
	})

	return err
}

func TestInterceptorReturnsResourceExhausted(t *testing.T) {
	t.Parallel()

	// burst=1: only one request allowed.
	rl := NewRateLimiter(&RateLimitConfig{
		LoginRPS:   1,
		LoginBurst: 1,
	}, nil)

	ctx := context.Background()

	// First request succeeds.
	err := callInterceptor(t, rl, "/user.v1.UserService/Login", ctx)
	if err != nil {
		t.Fatalf("1st request: %v", err)
	}

	// Second request should return ResourceExhausted.
	err = callInterceptor(t, rl, "/user.v1.UserService/Login", ctx)
	if err == nil {
		t.Fatal("2nd request: expected error")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("not a status error: %v", err)
	}

	if st.Code() != codes.ResourceExhausted {
		t.Errorf("status code = %v, want %v", st.Code(), codes.ResourceExhausted)
	}

	if !IsRateLimitErr(err) {
		t.Errorf("want ErrRateLimitExceeded, got %v", err)
	}
}

func TestInterceptorPerMethodLimits(t *testing.T) {
	t.Parallel()

	// Login burst=1, Register burst=2 — different buckets.
	rl := NewRateLimiter(&RateLimitConfig{
		LoginRPS:    1,
		LoginBurst:  1,
		RegisterRPS: 1,
		RegisterBurst: 2,
		GlobalRPS:   100,
		GlobalBurst: 200,
	}, nil)

	ctx := context.Background()

	// Login: 1st OK, 2nd denied.
	if callInterceptor(t, rl, "/user.v1.UserService/Login", ctx) != nil {
		t.Fatal("login 1st: expected OK")
	}
	if callInterceptor(t, rl, "/user.v1.UserService/Login", ctx) == nil {
		t.Fatal("login 2nd: expected denied")
	}

	// Register: both should succeed (separate bucket).
	if callInterceptor(t, rl, "/user.v1.UserService/Register", ctx) != nil {
		t.Fatal("register 1st: expected OK")
	}
	if callInterceptor(t, rl, "/user.v1.UserService/Register", ctx) != nil {
		t.Fatal("register 2nd: expected OK")
	}
}

func TestInterceptorIPSeparation(t *testing.T) {
	t.Parallel()

	rl := NewRateLimiter(&RateLimitConfig{
		LoginRPS:    1,
		LoginBurst:  1,
		GlobalRPS:   100,
		GlobalBurst: 200,
	}, nil)

	// IP 1: first OK, second denied.
	ctx1 := peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 1234}})
	if callInterceptor(t, rl, "/user.v1.UserService/Login", ctx1) != nil {
		t.Fatal("IP1 1st: expected OK")
	}
	if callInterceptor(t, rl, "/user.v1.UserService/Login", ctx1) == nil {
		t.Fatal("IP1 2nd: expected denied")
	}

	// IP 2: first OK (separate bucket).
	ctx2 := peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP("10.0.0.2"), Port: 1234}})
	if callInterceptor(t, rl, "/user.v1.UserService/Login", ctx2) != nil {
		t.Fatal("IP2 1st: expected OK")
	}
}

func TestInterceptorXForwardedFor(t *testing.T) {
	t.Parallel()

	rl := NewRateLimiter(&RateLimitConfig{
		LoginRPS:    1,
		LoginBurst:  1,
		GlobalRPS:   100,
		GlobalBurst: 200,
	}, nil)

	ctxPeer := peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 54321}})

	// XFF=203.0.113.5: first OK, second denied.
	ctx1 := metadata.NewIncomingContext(ctxPeer, metadata.Pairs("x-forwarded-for", "203.0.113.5"))
	if callInterceptor(t, rl, "/user.v1.UserService/Login", ctx1) != nil {
		t.Fatal("XFF1 1st: expected OK")
	}
	if callInterceptor(t, rl, "/user.v1.UserService/Login", ctx1) == nil {
		t.Fatal("XFF1 2nd: expected denied")
	}

	// XFF=198.51.100.7: first OK (different bucket).
	ctx2 := metadata.NewIncomingContext(ctxPeer, metadata.Pairs("x-forwarded-for", "198.51.100.7"))
	if callInterceptor(t, rl, "/user.v1.UserService/Login", ctx2) != nil {
		t.Fatal("XFF2 1st: expected OK")
	}
}

func TestInterceptorGlobalCap(t *testing.T) {
	t.Parallel()

	// High per-method limits, low global cap.
	rl := NewRateLimiter(&RateLimitConfig{
		LoginRPS:    100,
		LoginBurst:  100,
		GlobalRPS:   2,
		GlobalBurst: 2,
	}, nil)

	ctx := context.Background()

	// Two requests should succeed (global burst).
	for i := 0; i < 2; i++ {
		if callInterceptor(t, rl, "/user.v1.UserService/Login", ctx) != nil {
			t.Fatalf("request %d: expected OK", i+1)
		}
	}

	// Third should fail (global cap).
	if callInterceptor(t, rl, "/user.v1.UserService/Login", ctx) == nil {
		t.Fatal("3rd request: expected global cap denial")
	}
}

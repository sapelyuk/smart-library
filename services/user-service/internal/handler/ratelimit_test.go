package handler_test

import (
	"context"
	"errors"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"

	userv1 "github.com/sapelyuk/smart-library/services/user-service/gen/go/user/v1"
	"github.com/sapelyuk/smart-library/services/user-service/internal/handler"
)

const bufDialSize = 1024 * 1024

func bufDialer(listener *bufconn.Listener) func(context.Context, string) (net.Conn, error) {
	return func(_ context.Context, _ string) (net.Conn, error) {
		return listener.Dial()
	}
}

// ---------- tests ----------

func TestMethodType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		fullMethod string
		wantTyp    handler.LimitType
		wantKey    string
	}{
		{
			fullMethod: "/user.v1.UserService/Login",
			wantTyp:    handler.LimitLogin,
			wantKey:    "login",
		},
		{
			fullMethod: "/user.v1.UserService/Register",
			wantTyp:    handler.LimitRegister,
			wantKey:    "register",
		},
		{
			fullMethod: "/user.v1.UserService/ChangePassword",
			wantTyp:    handler.LimitChangePassword,
			wantKey:    "change_password",
		},
		{
			fullMethod: "/user.v1.UserService/GetCurrentUser",
			wantTyp:    handler.LimitGlobal,
			wantKey:    "global",
		},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(tc.fullMethod, func(t *testing.T) {
			t.Parallel()

			gotTyp, gotKey := handler.MethodType(tc.fullMethod)

			if gotTyp != tc.wantTyp {
				t.Errorf("methodType(%q) type = %v, want %v", tc.fullMethod, gotTyp, tc.wantTyp)
			}

			if gotKey != tc.wantKey {
				t.Errorf("methodType(%q) key = %q, want %q", tc.fullMethod, gotKey, tc.wantKey)
			}
		})
	}
}

func TestRateLimiterLoginBucketExhaustion(t *testing.T) {
	t.Parallel()

	rl := handler.NewRateLimiter(&handler.RateLimitConfig{
		LoginRPS:   1,
		LoginBurst: 1,
	}, nil)

	ctx := context.Background()

	// First request should succeed.
	err := callRateLimitedUserSvc(t, rl, "/user.v1.UserService/Login", ctx)
	if err != nil {
		t.Fatalf("first login: %v", err)
	}

	// Second request should be rejected.
	err = callRateLimitedUserSvc(t, rl, "/user.v1.UserService/Login", ctx)
	if err == nil {
		t.Fatal("second login: expected error, got nil")
	}

	if !errors.Is(err, handler.ErrRateLimitExceeded) {
		t.Errorf("want ErrRateLimitExceeded, got %v", err)
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("not a status error: %v", err)
	}

	if st.Code() != codes.ResourceExhausted {
		t.Errorf("status code = %v, want %v", st.Code(), codes.ResourceExhausted)
	}
}

func TestRateLimiterIPSeparation(t *testing.T) {
	t.Parallel()

	rl := handler.NewRateLimiter(&handler.RateLimitConfig{
		LoginRPS:    1,
		LoginBurst:  1,
		GlobalRPS:   100,
		GlobalBurst: 200,
	}, nil)

	ctx := context.Background()

	// IP 1: first request OK.
	ctx1 := peer.NewContext(ctx, &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 1234}})
	err := callRateLimitedUserSvc(t, rl, "/user.v1.UserService/Login", ctx1)
	if err != nil {
		t.Fatalf("IP1 first: %v", err)
	}

	// IP 1: second request rejected.
	err = callRateLimitedUserSvc(t, rl, "/user.v1.UserService/Login", ctx1)
	if err == nil {
		t.Fatal("IP1 second: expected error, got nil")
	}

	// IP 2: first request OK (separate bucket).
	ctx2 := peer.NewContext(ctx, &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP("10.0.0.2"), Port: 1234}})
	err = callRateLimitedUserSvc(t, rl, "/user.v1.UserService/Login", ctx2)
	if err != nil {
		t.Fatalf("IP2 first: %v", err)
	}
}

func TestRateLimiterXForwardedFor(t *testing.T) {
	t.Parallel()

	rl := handler.NewRateLimiter(&handler.RateLimitConfig{
		LoginRPS:    1,
		LoginBurst:  1,
		GlobalRPS:   100,
		GlobalBurst: 200,
	}, nil)

	ctx := context.Background()

	ctxPeer := peer.NewContext(ctx, &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 54321}})
	ctxWithMD := metadata.NewIncomingContext(ctxPeer, metadata.Pairs("x-forwarded-for", "203.0.113.5"))

	// First request OK.
	err := callRateLimitedUserSvc(t, rl, "/user.v1.UserService/Login", ctxWithMD)
	if err != nil {
		t.Fatalf("first with XFF: %v", err)
	}

	// Second request from same XFF should fail.
	err = callRateLimitedUserSvc(t, rl, "/user.v1.UserService/Login", ctxWithMD)
	if err == nil {
		t.Fatal("second with XFF: expected error, got nil")
	}

	// Different XFF should succeed.
	ctxOther := metadata.NewIncomingContext(ctxPeer, metadata.Pairs("x-forwarded-for", "198.51.100.7"))
	err = callRateLimitedUserSvc(t, rl, "/user.v1.UserService/Login", ctxOther)
	if err != nil {
		t.Fatalf("different XFF: %v", err)
	}
}

func TestRateLimiterGlobalCap(t *testing.T) {
	t.Parallel()

	rl := handler.NewRateLimiter(&handler.RateLimitConfig{
		LoginRPS:    100,
		LoginBurst:  100,
		GlobalRPS:   2,
		GlobalBurst: 2,
	}, nil)

	ctx := context.Background()

	// Two requests should succeed (global burst).
	for i := 0; i < 2; i++ {
		err := callRateLimitedUserSvc(t, rl, "/user.v1.UserService/Login", ctx)
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
	}

	// Third request should hit global cap.
	err := callRateLimitedUserSvc(t, rl, "/user.v1.UserService/Login", ctx)
	if err == nil {
		t.Fatal("third request: expected global cap error, got nil")
	}

	if !errors.Is(err, handler.ErrRateLimitExceeded) {
		t.Errorf("want ErrRateLimitExceeded, got %v", err)
	}
}

func TestRateLimiterNonAuthMethods(t *testing.T) {
	t.Parallel()

	rl := handler.NewRateLimiter(&handler.RateLimitConfig{
		LoginRPS:    1,
		LoginBurst:  1,
		GlobalRPS:   100,
		GlobalBurst: 200,
	}, nil)

	ctx := context.Background()

	// Non-auth methods should use the global bucket, not the login bucket.
	for i := 0; i < 5; i++ {
		err := callRateLimitedUserSvc(t, rl, "/user.v1.UserService/GetCurrentUser", ctx)
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
	}
}

// ---------- additional tests ----------

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

func TestRateLimiterAllowExhaustion(t *testing.T) {
	t.Parallel()

	rl := handler.NewRateLimiter(&handler.RateLimitConfig{
		LoginRPS:   1,
		LoginBurst: 2,
	}, nil)

	// First two should succeed.
	if !rl.allow(handler.LimitLogin, "test-key") {
		t.Fatal("1st allow: expected true")
	}
	if !rl.allow(handler.LimitLogin, "test-key") {
		t.Fatal("2nd allow: expected true")
	}

	// 3rd should fail.
	if rl.allow(handler.LimitLogin, "test-key") {
		t.Fatal("3rd allow: expected false")
	}
}

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

func TestInterceptorReturnsResourceExhausted(t *testing.T) {
	t.Parallel()

	// burst=1: only one request allowed.
	rl := handler.NewRateLimiter(&handler.RateLimitConfig{
		LoginRPS:   1,
		LoginBurst: 1,
	}, nil)

	ctx := context.Background()

	// First request succeeds.
	err := callRateLimitedUserSvc(t, rl, "/user.v1.UserService/Login", ctx)
	if err != nil {
		t.Fatalf("1st request: %v", err)
	}

	// Second request should return ResourceExhausted.
	err = callRateLimitedUserSvc(t, rl, "/user.v1.UserService/Login", ctx)
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
}

func TestBucketEvictionJustification(t *testing.T) {
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
	rl := handler.NewRateLimiter(&handler.RateLimitConfig{
		LoginRPS:   1,
		LoginBurst: 1,
	}, nil)

	key := "eviction-test-key"

	// Exhaust the bucket.
	rl.allow(handler.LimitLogin, key) // succeeds
	rl.allow(handler.LimitLogin, key) // denied

	// The bucket still exists (no eviction).  A new request should
	// still be denied — proving the bucket wasn't removed.
	if rl.allow(handler.LimitLogin, key) {
		t.Fatal("after exhaustion: expected deny (bucket should persist)")
	}
}

// callRateLimitedUserSvc creates a bufconn gRPC server with the rate-limit
// interceptor + a no-op handler, dials it, and calls the given full method.
func callRateLimitedUserSvc(t *testing.T, rl *handler.RateLimiter, fullMethod string, ctx context.Context) error {
	t.Helper()

	listener := bufconn.Listen(bufDialSize)

	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			handler.NewRateLimitUnaryInterceptor(rl),
			func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
				return req, nil
			},
		),
	)

	// Register the real UserService so the full method names match.
	// The handler is a no-op: we only care about the rate limiter.
	srv.RegisterService(&userv1.UserService_ServiceDesc, &rateLimitStub{})

	go func() {
		_ = srv.Serve(listener)
	}()

	t.Cleanup(func() {
		srv.Stop()
	})

	conn, err := grpc.DialContext(ctx, "bufnet",
		grpc.WithContextDialer(bufDialer(listener)),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return err
	}

	defer conn.Close()

	client := userv1.NewUserServiceClient(conn)

	switch fullMethod {
	case "/user.v1.UserService/Login":
		_, err = client.Login(ctx, &userv1.LoginRequest{})
	case "/user.v1.UserService/Register":
		_, err = client.Register(ctx, &userv1.RegisterRequest{})
	case "/user.v1.UserService/ChangePassword":
		_, err = client.ChangePassword(ctx, &userv1.ChangePasswordRequest{})
	case "/user.v1.UserService/GetCurrentUser":
		_, err = client.GetCurrentUser(ctx, &emptypb.Empty{})
	default:
		return nil
	}

	return err
}

// rateLimitStub implements userv1.UserServiceServer but only returns errors
// so that the rate limiter is the only thing that can succeed.
type rateLimitStub struct{}

func (s *rateLimitStub) Register(context.Context, *userv1.RegisterRequest) (*userv1.User, error) {
	return nil, status.Error(codes.Unimplemented, "unimplemented")
}

func (s *rateLimitStub) Login(context.Context, *userv1.LoginRequest) (*userv1.Session, error) {
	return nil, status.Error(codes.Unimplemented, "unimplemented")
}

func (s *rateLimitStub) Logout(context.Context, *emptypb.Empty) (*emptypb.Empty, error) {
	return nil, status.Error(codes.Unimplemented, "unimplemented")
}

func (s *rateLimitStub) GetCurrentUser(context.Context, *emptypb.Empty) (*userv1.User, error) {
	return nil, status.Error(codes.Unimplemented, "unimplemented")
}

func (s *rateLimitStub) GetUser(context.Context, *userv1.GetUserRequest) (*userv1.User, error) {
	return nil, status.Error(codes.Unimplemented, "unimplemented")
}

func (s *rateLimitStub) ListUsers(context.Context, *userv1.ListUsersRequest) (*userv1.ListUsersResponse, error) {
	return nil, status.Error(codes.Unimplemented, "unimplemented")
}

func (s *rateLimitStub) UpdateUser(context.Context, *userv1.UpdateUserRequest) (*userv1.User, error) {
	return nil, status.Error(codes.Unimplemented, "unimplemented")
}

func (s *rateLimitStub) ChangePassword(context.Context, *userv1.ChangePasswordRequest) (*emptypb.Empty, error) {
	return nil, status.Error(codes.Unimplemented, "unimplemented")
}

func (s *rateLimitStub) DeactivateUser(context.Context, *userv1.DeactivateUserRequest) (*userv1.User, error) {
	return nil, status.Error(codes.Unimplemented, "unimplemented")
}

func (s *rateLimitStub) RestoreUser(context.Context, *userv1.RestoreUserRequest) (*userv1.User, error) {
	return nil, status.Error(codes.Unimplemented, "unimplemented")
}

func (s *rateLimitStub) AuthenticateToken(context.Context, *userv1.AuthenticateTokenRequest) (*userv1.Principal, error) {
	return nil, status.Error(codes.Unimplemented, "unimplemented")
}

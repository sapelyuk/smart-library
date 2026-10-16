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
		_, err = client.GetCurrentUser(ctx, &userv1.GetCurrentUserRequest{})
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

func (s *rateLimitStub) Logout(context.Context, *userv1.LogoutRequest) (*userv1.Empty, error) {
	return nil, status.Error(codes.Unimplemented, "unimplemented")
}

func (s *rateLimitStub) GetCurrentUser(context.Context, *userv1.GetCurrentUserRequest) (*userv1.User, error) {
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

func (s *rateLimitStub) ChangePassword(context.Context, *userv1.ChangePasswordRequest) (*userv1.Empty, error) {
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

package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/sapelyuk/smart-library/services/loan-service/internal/auth"
	"github.com/sapelyuk/smart-library/services/loan-service/internal/domain"
)

// stubVerifier answers with a canned principal or error.
type stubVerifier struct {
	principal domain.Principal
	err       error
	tokens    []string
}

func (v *stubVerifier) Verify(_ context.Context, token string) (domain.Principal, error) {
	v.tokens = append(v.tokens, token)

	if v.err != nil {
		return domain.Principal{}, v.err
	}

	return v.principal, nil
}

// incomingContext builds a context carrying the given authorization header.
func incomingContext(header string) context.Context {
	md := metadata.MD{}

	if header != "" {
		md.Set("authorization", header)
	}

	return metadata.NewIncomingContext(context.Background(), md)
}

// runInterceptor drives the interceptor and returns what the handler saw.
func runInterceptor(t *testing.T, verifier auth.Verifier, ctx context.Context, method string) (domain.Principal, error) {
	t.Helper()

	var seen domain.Principal

	handler := func(ctx context.Context, _ any) (any, error) {
		principal, err := auth.PrincipalFrom(ctx)
		if err != nil {
			return nil, err
		}

		seen = principal

		return "ok", nil
	}

	_, err := auth.NewUnaryServerInterceptor(verifier)(ctx, nil, &grpc.UnaryServerInfo{FullMethod: method}, handler)

	return seen, err
}

func TestInterceptorResolvesPrincipal(t *testing.T) {
	t.Parallel()

	userID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	verifier := &stubVerifier{principal: domain.Principal{UserID: userID, Role: domain.RoleLibrarian}}

	principal, err := runInterceptor(t, verifier, incomingContext("Bearer token-abc"), "/loan.v1.LoanService/List")
	if err != nil {
		t.Fatalf("interceptor: unexpected error: %v", err)
	}

	if principal.UserID != userID || principal.Role != domain.RoleLibrarian {
		t.Errorf("principal = %+v, want the one returned by the verifier", principal)
	}

	if len(verifier.tokens) != 1 || verifier.tokens[0] != "token-abc" {
		t.Errorf("tokens = %v, want the bare token without the scheme", verifier.tokens)
	}
}

func TestInterceptorAcceptsLowercaseScheme(t *testing.T) {
	t.Parallel()

	verifier := &stubVerifier{principal: domain.Principal{UserID: uuid.New()}}

	if _, err := runInterceptor(t, verifier, incomingContext("bearer token-abc"), "/loan.v1.LoanService/List"); err != nil {
		t.Fatalf("interceptor: unexpected error: %v", err)
	}
}

func TestInterceptorRejectsMissingToken(t *testing.T) {
	t.Parallel()

	verifier := &stubVerifier{principal: domain.Principal{UserID: uuid.New()}}

	_, err := runInterceptor(t, verifier, incomingContext(""), "/loan.v1.LoanService/List")
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", status.Code(err))
	}

	if len(verifier.tokens) != 0 {
		t.Error("the verifier must not be called without a token")
	}
}

func TestInterceptorRejectsMalformedHeader(t *testing.T) {
	t.Parallel()

	verifier := &stubVerifier{principal: domain.Principal{UserID: uuid.New()}}

	for _, header := range []string{"token-abc", "Basic dXNlcjpwYXNz", "Bearer "} {
		if _, err := runInterceptor(t, verifier, incomingContext(header), "/loan.v1.LoanService/List"); status.Code(err) != codes.Unauthenticated {
			t.Errorf("header %q: code = %v, want Unauthenticated", header, status.Code(err))
		}
	}
}

func TestInterceptorMapsVerifierFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want codes.Code
	}{
		{
			name: "status error is kept",
			err:  status.Error(codes.Unauthenticated, "invalid token"),
			want: codes.Unauthenticated,
		},
		{
			name: "plain error becomes unauthenticated",
			err:  errors.New("connection refused"),
			want: codes.Unauthenticated,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			verifier := &stubVerifier{err: tc.err}

			_, err := runInterceptor(t, verifier, incomingContext("Bearer token-abc"), "/loan.v1.LoanService/List")
			if status.Code(err) != tc.want {
				t.Fatalf("code = %v, want %v", status.Code(err), tc.want)
			}
		})
	}
}

// TestInterceptorLeavesHealthPublic proves the container probe works without a
// token.
func TestInterceptorLeavesHealthPublic(t *testing.T) {
	t.Parallel()

	verifier := &stubVerifier{err: errors.New("should not be called")}

	_, err := auth.NewUnaryServerInterceptor(verifier)(
		incomingContext(""),
		nil,
		&grpc.UnaryServerInfo{FullMethod: "/grpc.health.v1.Health/Check"},
		func(context.Context, any) (any, error) { return "serving", nil },
	)
	if err != nil {
		t.Fatalf("health check: unexpected error: %v", err)
	}

	if len(verifier.tokens) != 0 {
		t.Error("the health check must not authenticate")
	}
}

func TestPrincipalFromWithoutPrincipal(t *testing.T) {
	t.Parallel()

	if _, err := auth.PrincipalFrom(context.Background()); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("want %v, got %v", domain.ErrUnauthenticated, err)
	}
}

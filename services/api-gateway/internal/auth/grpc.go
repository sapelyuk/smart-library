package auth

import (
	"context"
	"fmt"

	"google.golang.org/grpc"

	userv1 "github.com/sapelyuk/smart-library/services/user-service/gen/go/user/v1"
)

// grpcVerifier resolves a bearer token by calling AuthenticateToken on
// user-service over gRPC.
type grpcVerifier struct {
	client userv1.UserServiceClient
}

// NewGRPCVerifier returns a Verifier that calls AuthenticateToken on
// user-service through the supplied gRPC connection.
//
// The AuthenticateToken method is marked internal in the user-service contract
// (no REST route); only the platform's own services call it.
func NewGRPCVerifier(conn grpc.ClientConnInterface) Verifier {
	return &grpcVerifier{client: userv1.NewUserServiceClient(conn)}
}

// Verify calls AuthenticateToken and maps the response to Principal.
//
// A gRPC error (Unauthenticated, PermissionDenied, ...) is returned as-is so
// the caller can decide the HTTP status; the middleware maps all verification
// failures to 401.
func (v *grpcVerifier) Verify(ctx context.Context, token string) (Principal, error) {
	resp, err := v.client.AuthenticateToken(ctx, &userv1.AuthenticateTokenRequest{
		Token: token,
	})
	if err != nil {
		return Principal{}, fmt.Errorf("auth: authenticate token: %w", err)
	}

	return Principal{
		UserID: resp.GetUserId(),
		Email:  resp.GetEmail(),
		Role:   resp.GetRole().String(),
	}, nil
}

// Package usersvc resolves the bearer token of an incoming call against
// user-service.
//
// This service never parses or trusts a token on its own: it asks user-service,
// which owns the session, and turns the answer into a domain.Principal. The
// contract method AuthenticateToken is marked internal in the user-service proto
// (no REST route) — only platform services call it.
package usersvc

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/sapelyuk/smart-library/services/ai-service/internal/domain"
	userv1 "github.com/sapelyuk/smart-library/services/user-service/gen/go/user/v1"
)

// Verifier resolves a bearer token into the principal that owns it.
type Verifier interface {
	Verify(ctx context.Context, token string) (domain.Principal, error)
}

// grpcVerifier is the gRPC backed Verifier.
type grpcVerifier struct {
	client userv1.UserServiceClient
}

var _ Verifier = (*grpcVerifier)(nil)

// NewVerifier returns a Verifier that calls AuthenticateToken on user-service
// through the supplied gRPC connection.
func NewVerifier(conn grpc.ClientConnInterface) *grpcVerifier {
	return &grpcVerifier{client: userv1.NewUserServiceClient(conn)}
}

// Verify calls AuthenticateToken and maps the answer onto a Principal.
//
// A gRPC status error is returned unchanged so the interceptor can tell an
// unauthenticated token from a transport failure.
func (v *grpcVerifier) Verify(ctx context.Context, token string) (domain.Principal, error) {
	principal, err := v.client.AuthenticateToken(ctx, &userv1.AuthenticateTokenRequest{Token: token})
	if err != nil {
		return domain.Principal{}, fmt.Errorf("usersvc: authenticate token: %w", err)
	}

	if principal == nil {
		return domain.Principal{}, status.Error(codes.Unauthenticated, "user-service returned no principal")
	}

	userID, err := uuid.Parse(principal.GetUserId())
	if err != nil {
		return domain.Principal{}, fmt.Errorf("usersvc: parse user id: %w", err)
	}

	sessionID, err := uuid.Parse(principal.GetSessionId())
	if err != nil {
		return domain.Principal{}, fmt.Errorf("usersvc: parse session id: %w", err)
	}

	return domain.Principal{
		UserID:    userID,
		SessionID: sessionID,
		Email:     principal.GetEmail(),
		Role:      mapRole(principal.GetRole()),
	}, nil
}

// mapRole translates the proto enum into the domain role: the proto spells the
// values ROLE_READER / ROLE_LIBRARIAN, the domain drops the prefix.
func mapRole(role userv1.Role) domain.Role {
	return domain.Role(strings.TrimPrefix(role.String(), "ROLE_"))
}

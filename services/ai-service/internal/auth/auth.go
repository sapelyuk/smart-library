// Package auth turns the bearer token of a call into the principal the AI service
// works on behalf of.
//
// The token is not validated locally: it is checked through user-service over gRPC
// (AuthenticateToken), exactly like the API gateway does. The identity that service
// returns becomes the domain.Principal carried on the context of the call, which
// every use case then demands as an explicit argument.
package auth

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/sapelyuk/smart-library/services/ai-service/internal/domain"
)

// contextKey keys the request scoped values this package puts on the context.
type contextKey int

const principalKey contextKey = iota

// mdAuthorization is the metadata key gRPC lowercases every header into. The
// grpc-gateway forwards the Authorization header of a REST call under exactly this
// key, which is why one interceptor serves both transports.
const mdAuthorization = "authorization"

// bearerPrefix is the scheme the contract documents. The comparison is case
// insensitive because the HTTP spec defines the scheme that way.
const bearerPrefix = "bearer "

// publicMethods are the RPCs that run without a principal. The AI service exposes
// none of its own: even a recommendation is issued by a signed-in reader. Only the
// standard health service is exempt, so a container probe keeps working without a
// token.
var publicMethods = map[string]struct{}{
	"/grpc.health.v1.Health/Check": {},
	"/grpc.health.v1.Health/Watch": {},
}

// Verifier resolves a bearer token into the principal behind it. The gRPC
// implementation calls AuthenticateToken on user-service; tests use a stub.
type Verifier interface {
	Verify(ctx context.Context, token string) (domain.Principal, error)
}

// NewUnaryServerInterceptor returns the interceptor that turns the bearer token of
// a call into the principal every use case demands.
func NewUnaryServerInterceptor(verifier Verifier) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		if _, isPublic := publicMethods[info.FullMethod]; isPublic {
			return handler(ctx, req)
		}

		token, ok := bearerToken(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, domain.ErrUnauthenticated.Error())
		}

		principal, err := verifier.Verify(ctx, token)
		if err != nil {
			return nil, toStatus(err)
		}

		return handler(WithPrincipal(ctx, principal), req)
	}
}

// toStatus translates an error of the authentication path into a status error.
// The interceptor runs before any handler, so it cannot leave the translation to
// the handler: an unclassified error would reach the client as "unknown", which the
// REST gateway renders as 500.
func toStatus(err error) error {
	if st, ok := status.FromError(err); ok && st.Code() != codes.Unknown {
		return st.Err()
	}

	return status.Error(codes.Unauthenticated, err.Error())
}

// bearerToken pulls the credential out of the metadata of the call.
func bearerToken(ctx context.Context) (string, bool) {
	values, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", false
	}

	// A request that carries the header twice is not a request this service guesses
	// about: which of the two tokens would be the caller?
	raw := values.Get(mdAuthorization)
	if len(raw) != 1 {
		return "", false
	}

	value := strings.TrimSpace(raw[0])
	if len(value) < len(bearerPrefix) || !strings.EqualFold(value[:len(bearerPrefix)], bearerPrefix) {
		return "", false
	}

	token := strings.TrimSpace(value[len(bearerPrefix):])

	return token, token != ""
}

// WithPrincipal puts the resolved caller on the context of the call.
func WithPrincipal(ctx context.Context, principal domain.Principal) context.Context {
	return context.WithValue(ctx, principalKey, principal)
}

// PrincipalFrom reads the caller back. A missing principal is
// domain.ErrUnauthenticated rather than a panic: an RPC added to the service
// without being classified in publicMethods must fail closed.
func PrincipalFrom(ctx context.Context) (domain.Principal, error) {
	principal, ok := ctx.Value(principalKey).(domain.Principal)
	if !ok || principal.UserID == uuid.Nil {
		return domain.Principal{}, domain.ErrUnauthenticated
	}

	return principal, nil
}

// Package handler adapts the transports of the user service to its use cases.
//
// The package owns three things: the conversion between the protobuf contract and
// the domain model, the translation of domain errors into gRPC codes, and the
// resolution of the bearer token into the principal that every use case demands.
// No business rule lives here; a rule that appears in this package is a rule that
// the REST transport and the gRPC transport will not share.
package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/google/uuid"

	userv1 "github.com/sapelyuk/smart-library/services/user-service/gen/go/user/v1"
	"github.com/sapelyuk/smart-library/services/user-service/internal/domain"
	"github.com/sapelyuk/smart-library/services/user-service/internal/service"
)

// Handler implements userv1.UserServiceServer on top of the use case layer.
type Handler struct {
	userv1.UnimplementedUserServiceServer

	service *service.Service
	log     *slog.Logger
}

// New wires the transport adapters. The handler is one type because the two
// halves of the API — the authentication and the account management — share the
// same use case service and the same mapping rules.
func New(svc *service.Service, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}

	return &Handler{service: svc, log: log}
}

// errInvalidID reports a malformed UUID on the wire. It is a transport level
// concern, so it lives here rather than in the domain.
var errInvalidID = errors.New("id must be a UUID")

// parseID decodes an identifier that arrived over the wire.
func parseID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: %q", errInvalidID, raw)
	}

	return id, nil
}

// codeByError maps a domain error onto the status code of the contract. The
// table is a slice rather than a map because the order carries meaning: an error
// that wraps two sentinels must resolve to the more specific one listed first.
var codeByError = []struct {
	err  error
	code codes.Code
}{
	{errInvalidID, codes.InvalidArgument},
	{domain.ErrInvalidEmail, codes.InvalidArgument},
	{domain.ErrEmptyFullName, codes.InvalidArgument},
	{domain.ErrInvalidFullName, codes.InvalidArgument},
	{domain.ErrInvalidPhone, codes.InvalidArgument},
	{domain.ErrInvalidRole, codes.InvalidArgument},
	{domain.ErrInvalidStatus, codes.InvalidArgument},
	{domain.ErrPasswordTooShort, codes.InvalidArgument},
	{domain.ErrPasswordTooLong, codes.InvalidArgument},
	{domain.ErrPasswordTooWeak, codes.InvalidArgument},
	{domain.ErrEmailAlreadyExists, codes.AlreadyExists},
	{domain.ErrUserNotFound, codes.NotFound},
	{domain.ErrSessionNotFound, codes.NotFound},
	{domain.ErrInvalidCredentials, codes.Unauthenticated},
	{domain.ErrUnauthenticated, codes.Unauthenticated},
	{domain.ErrSessionExpired, codes.Unauthenticated},
	{domain.ErrPermissionDenied, codes.PermissionDenied},
	{domain.ErrDeactivated, codes.PermissionDenied},
	{domain.ErrSelfLockout, codes.FailedPrecondition},
	{domain.ErrRateLimitExceeded, codes.ResourceExhausted},
}

// toGRPC turns a use case error into a status error.
//
// The message the client sees is the text of the domain sentinel, never the text
// of the wrapped chain: a database constraint name or a connection string is not
// something an API client should receive. An unmapped error is an Internal and is
// logged with its full detail, because an unmapped error is a bug.
func (h *Handler) toGRPC(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}

	// An error that already carries a code was produced by the interceptor or by
	// a nested call; re-wrapping it would lose the code.
	if st, ok := status.FromError(err); ok && st.Code() != codes.Unknown {
		return st.Err()
	}

	if code, mapped := codeFor(err); mapped {
		return status.Error(code, err.Error())
	}

	h.log.ErrorContext(ctx, "unmapped error", "error", err)

	return status.Error(codes.Internal, "internal error")
}

// codeFor resolves a domain error into its status code.
//
// The interceptor needs the same answer as a handler does, and it has no logger
// to report an unmapped error to, so the lookup is a function of its own rather
// than a method on the handler. The second return value tells the caller whether
// the error was recognised at all.
func codeFor(err error) (codes.Code, bool) {
	for _, mapping := range codeByError {
		if errors.Is(err, mapping.err) {
			return mapping.code, true
		}
	}

	return codes.Internal, false
}

// protoUser converts an account into the contract message.
//
// PasswordHash has no counterpart in the message and that is the point: the list
// of mapped fields is the audit trail of what the service is willing to say about
// an account.
func protoUser(user *domain.User) *userv1.User {
	if user == nil {
		return nil
	}

	return &userv1.User{
		Id:          user.ID.String(),
		Email:       user.Email.String(),
		FullName:    user.FullName,
		Role:        protoRole(user.Role),
		Status:      protoStatus(user.Status),
		Phone:       user.Phone,
		CreatedAt:   protoTime(user.CreatedAt),
		UpdatedAt:   protoTime(user.UpdatedAt),
		LastLoginAt: protoTime(user.LastLogin),
	}
}

// protoUsers converts a page of accounts.
func protoUsers(users []*domain.User) []*userv1.User {
	converted := make([]*userv1.User, 0, len(users))

	for _, user := range users {
		converted = append(converted, protoUser(user))
	}

	return converted
}

// protoCredentials converts a successful sign in into the session message. The
// token appears here and nowhere else in the API.
func protoCredentials(credentials *service.Credentials) *userv1.Session {
	return &userv1.Session{
		Id:          credentials.Session.ID.String(),
		UserId:      credentials.User.ID.String(),
		AccessToken: credentials.Token,
		CreatedAt:   protoTime(credentials.Session.CreatedAt),
		ExpiresAt:   protoTime(credentials.Session.ExpiresAt),
	}
}

// protoPrincipal converts the caller for AuthenticateToken, the answer the other
// services of the platform read a token through.
func protoPrincipal(principal domain.Principal, expiresAt time.Time) *userv1.Principal {
	return &userv1.Principal{
		UserId:           principal.UserID.String(),
		Email:            principal.Email.String(),
		Role:             protoRole(principal.Role),
		SessionId:        principal.SessionID.String(),
		SessionExpiresAt: protoTime(expiresAt),
	}
}

func protoRole(role domain.Role) userv1.Role {
	switch role {
	case domain.RoleReader:
		return userv1.Role_ROLE_READER
	case domain.RoleLibrarian:
		return userv1.Role_ROLE_LIBRARIAN
	default:
		return userv1.Role_ROLE_UNSPECIFIED
	}
}

// roleFromProto decodes the role of a request. Unspecified is refused rather than
// defaulted: a client that forgot the field must learn about it instead of
// silently creating a reader.
func roleFromProto(role userv1.Role) (domain.Role, error) {
	switch role {
	case userv1.Role_ROLE_READER:
		return domain.RoleReader, nil
	case userv1.Role_ROLE_LIBRARIAN:
		return domain.RoleLibrarian, nil
	default:
		return "", fmt.Errorf("%w: %s", domain.ErrInvalidRole, role)
	}
}

func protoStatus(userStatus domain.UserStatus) userv1.UserStatus {
	switch userStatus {
	case domain.UserStatusActive:
		return userv1.UserStatus_USER_STATUS_ACTIVE
	case domain.UserStatusDeactivated:
		return userv1.UserStatus_USER_STATUS_DEACTIVATED
	default:
		return userv1.UserStatus_USER_STATUS_UNSPECIFIED
	}
}

func statusFromProto(userStatus userv1.UserStatus) (domain.UserStatus, error) {
	switch userStatus {
	case userv1.UserStatus_USER_STATUS_ACTIVE:
		return domain.UserStatusActive, nil
	case userv1.UserStatus_USER_STATUS_DEACTIVATED:
		return domain.UserStatusDeactivated, nil
	default:
		return "", fmt.Errorf("%w: %s", domain.ErrInvalidStatus, userStatus)
	}
}

// protoTime renders a moment for the wire. A zero moment becomes no field at all,
// which the REST mapping shows as an absent value rather than year one.
func protoTime(moment time.Time) *timestamppb.Timestamp {
	if moment.IsZero() {
		return nil
	}

	return timestamppb.New(moment)
}

package domain

import "errors"

// Domain errors of the user service. Wrap them with fmt.Errorf("%w") on the way
// up and match with errors.Is in the transport layer.
var (
	ErrUserNotFound = errors.New("user not found")

	ErrEmailAlreadyExists = errors.New("user with this email already exists")
	ErrInvalidEmail       = errors.New("invalid email")
	ErrEmptyFullName      = errors.New("full name must not be empty")
	ErrInvalidFullName    = errors.New("full name must contain only letters, digits, spaces, hyphens and apostrophes")
	ErrInvalidPhone       = errors.New("phone must be 10 to 15 digits, optional leading +")
	ErrInvalidRole        = errors.New("invalid role")
	ErrInvalidStatus      = errors.New("invalid user status")

	ErrPasswordTooShort = errors.New("password is too short")
	ErrPasswordTooLong  = errors.New("password is too long")
	ErrPasswordTooWeak  = errors.New("password must mix at least three character classes: lower, upper, digit, symbol")

	// ErrInvalidCredentials is the only answer a wrong sign in attempt may
	// produce: it never tells an unknown account apart from a wrong password.
	ErrInvalidCredentials = errors.New("invalid email or password")

	ErrDeactivated = errors.New("user is deactivated")

	// ErrUnauthenticated is returned when the bearer token is missing,
	// malformed, unknown, expired or revoked.
	ErrUnauthenticated = errors.New("unauthenticated")

	// ErrPermissionDenied is returned when the authenticated caller is not
	// allowed to perform the operation.
	ErrPermissionDenied = errors.New("permission denied")

	// ErrSelfLockout protects the service from a librarian who removes the
	// last way back into the system.
	ErrSelfLockout = errors.New("a librarian cannot revoke own rights")

	ErrSessionNotFound = errors.New("session not found")
	ErrSessionExpired  = errors.New("session expired")

	// ErrRateLimitExceeded is returned when the caller has exceeded the
	// allowed request rate.  The transport layer must translate this into
	// gRPC codes.ResourceExhausted (HTTP 429).
	ErrRateLimitExceeded = errors.New("rate limit exceeded")
)

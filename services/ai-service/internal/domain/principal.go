package domain

import (
	"fmt"

	"github.com/google/uuid"
)

// Role of a caller, mirroring the roles user-service issues.
type Role string

const (
	RoleReader    Role = "READER"
	RoleLibrarian Role = "LIBRARIAN"
)

// Principal is the authenticated caller: the identity resolved from a bearer
// token by the auth interceptor through user-service.
//
// Every use case takes it as an explicit argument instead of digging it out of
// the context, so the authorization rules are visible in the signature of the
// method and are identical for gRPC and for REST.
type Principal struct {
	UserID    uuid.UUID
	SessionID uuid.UUID
	Email     string
	Role      Role
}

// IsLibrarian reports whether the caller holds the librarian role.
func (p Principal) IsLibrarian() bool {
	return p.Role == RoleLibrarian
}

// RequireLibrarian guards the index maintenance: reindexing and dropping books
// change what every reader sees, so they belong to the staff. Recommending is
// not guarded — every authenticated reader may ask.
func (p Principal) RequireLibrarian(action string) error {
	if !p.IsLibrarian() {
		return fmt.Errorf("%w: %s requires the librarian role", ErrPermissionDenied, action)
	}

	return nil
}

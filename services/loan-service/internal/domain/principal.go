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
// Every privileged use case takes it as an explicit argument instead of digging
// it out of the context, so the authorization rules are visible in the signature
// of the method and are the same for gRPC and for REST.
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

// IsSelf reports whether the target reader is the caller.
func (p Principal) IsSelf(target uuid.UUID) bool {
	return p.UserID == target
}

// RequireLibrarian guards the operations that belong to the staff only.
func (p Principal) RequireLibrarian(action string) error {
	if !p.IsLibrarian() {
		return fmt.Errorf("%w: %s requires the librarian role", ErrPermissionDenied, action)
	}

	return nil
}

// RequireBorrowAccess guards Borrow: a reader borrows only for own account, a
// librarian may lend to any reader.
func (p Principal) RequireBorrowAccess(readerID uuid.UUID) error {
	if p.IsSelf(readerID) || p.IsLibrarian() {
		return nil
	}

	return fmt.Errorf("%w: a reader may borrow only for own account", ErrPermissionDenied)
}

// RequireLoanAccess guards the operations on one loan: the owning reader or a
// librarian may act, nobody else.
func (p Principal) RequireLoanAccess(readerID uuid.UUID, action string) error {
	if p.IsSelf(readerID) || p.IsLibrarian() {
		return nil
	}

	return fmt.Errorf("%w: %s needs the owning reader or a librarian", ErrPermissionDenied, action)
}

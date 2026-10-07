package domain_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/sapelyuk/smart-library/services/loan-service/internal/domain"
)

func TestPrincipalRequireBorrowAccess(t *testing.T) {
	t.Parallel()

	other := uuid.MustParse("44444444-4444-4444-4444-444444444444")

	tests := []struct {
		name    string
		role    domain.Role
		user    uuid.UUID
		target  uuid.UUID
		wantErr error
	}{
		{
			name:   "reader borrows for own account",
			role:   domain.RoleReader,
			user:   readerID,
			target: readerID,
		},
		{
			name:    "reader borrows for another account",
			role:    domain.RoleReader,
			user:    readerID,
			target:  other,
			wantErr: domain.ErrPermissionDenied,
		},
		{
			name:   "librarian borrows for another account",
			role:   domain.RoleLibrarian,
			user:   other,
			target: readerID,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			principal := domain.Principal{UserID: tc.user, Role: tc.role}

			err := principal.RequireBorrowAccess(tc.target)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestPrincipalRequireLoanAccess(t *testing.T) {
	t.Parallel()

	other := uuid.MustParse("55555555-5555-5555-5555-555555555555")

	tests := []struct {
		name    string
		role    domain.Role
		user    uuid.UUID
		owner   uuid.UUID
		wantErr error
	}{
		{
			name:  "owner",
			role:  domain.RoleReader,
			user:  readerID,
			owner: readerID,
		},
		{
			name:    "another reader",
			role:    domain.RoleReader,
			user:    other,
			owner:   readerID,
			wantErr: domain.ErrPermissionDenied,
		},
		{
			name:  "librarian",
			role:  domain.RoleLibrarian,
			user:  other,
			owner: readerID,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			principal := domain.Principal{UserID: tc.user, Role: tc.role}

			err := principal.RequireLoanAccess(tc.owner, "returning a loan")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestPrincipalRequireLibrarian(t *testing.T) {
	t.Parallel()

	reader := domain.Principal{UserID: readerID, Role: domain.RoleReader}
	if err := reader.RequireLibrarian("listing every loan"); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("reader error = %v, want ErrPermissionDenied", err)
	}

	librarian := domain.Principal{UserID: readerID, Role: domain.RoleLibrarian}
	if err := librarian.RequireLibrarian("listing every loan"); err != nil {
		t.Fatalf("librarian error = %v, want nil", err)
	}
}

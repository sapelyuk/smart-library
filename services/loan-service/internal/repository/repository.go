// Package repository defines the storage contracts of the loan service.
// Implementations translate storage failures into domain errors so that the
// upper layers never depend on a particular database driver.
package repository

import (
	"context"

	"github.com/google/uuid"

	"github.com/sapelyuk/smart-library/services/loan-service/internal/domain"
)

// LoanRepository stores the lending history of the library.
type LoanRepository interface {
	// Create inserts a new loan. A copy that already has an open loan is
	// refused with domain.ErrCopyAlreadyOnLoan.
	Create(ctx context.Context, loan *domain.Loan) error

	// GetByID returns the loan by its identifier.
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Loan, error)

	// Update writes the mutable columns (due date, return date, status) of a
	// loan.
	Update(ctx context.Context, loan *domain.Loan) error

	// List returns a page of loans and the total number of matching records.
	List(ctx context.Context, filter domain.LoanFilter) ([]*domain.Loan, int, error)
}

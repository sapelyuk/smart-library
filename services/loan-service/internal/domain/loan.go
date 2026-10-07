// Package domain holds the entities, the value objects and the business rules
// of the loan service. It depends on nothing but the standard library and the
// UUID package: no transport, no database, no other service.
package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// LoanStatus is the persisted lifecycle state of a loan.
//
// Only ACTIVE and RETURNED are stored. OVERDUE is derived from the due date and
// the return date, which is why it is not a value of this type.
type LoanStatus string

const (
	LoanStatusActive   LoanStatus = "ACTIVE"
	LoanStatusReturned LoanStatus = "RETURNED"
)

// Valid reports whether the status is one a row may carry.
func (s LoanStatus) Valid() bool {
	switch s {
	case LoanStatusActive, LoanStatusReturned:
		return true
	default:
		return false
	}
}

// EffectiveStatus is the status reported to clients. It adds the derived
// OVERDUE state on top of the persisted LoanStatus.
type EffectiveStatus string

const (
	StatusActive   EffectiveStatus = "ACTIVE"
	StatusReturned EffectiveStatus = "RETURNED"
	StatusOverdue  EffectiveStatus = "OVERDUE"
)

// Lending period bounds.
const (
	// DefaultLoanPeriod is used when the caller does not ask for another one.
	DefaultLoanPeriod = 14 * 24 * time.Hour

	// MinLoanPeriod and MaxLoanPeriod bound a single lending period.
	MinLoanPeriod = 1 * 24 * time.Hour
	MaxLoanPeriod = 90 * 24 * time.Hour
)

// Loan records who borrowed which physical copy of which book, until when and
// when it came back. It is the aggregate root of the lending domain.
type Loan struct {
	ID         uuid.UUID
	ReaderID   uuid.UUID
	BookID     uuid.UUID
	CopyID     uuid.UUID
	BorrowedAt time.Time
	DueAt      time.Time
	ReturnedAt *time.Time
	Status     LoanStatus
}

// NewLoan opens a loan of the given period, starting at now.
func NewLoan(readerID, bookID, copyID uuid.UUID, now time.Time, period time.Duration) (*Loan, error) {
	if readerID == uuid.Nil {
		return nil, ErrInvalidReaderID
	}

	if bookID == uuid.Nil {
		return nil, ErrInvalidBookID
	}

	if copyID == uuid.Nil {
		return nil, ErrInvalidCopyID
	}

	if period < MinLoanPeriod || period > MaxLoanPeriod {
		return nil, fmt.Errorf("%w: %s, expected %s..%s", ErrInvalidLoanPeriod, period, MinLoanPeriod, MaxLoanPeriod)
	}

	now = now.UTC()

	return &Loan{
		ID:         uuid.New(),
		ReaderID:   readerID,
		BookID:     bookID,
		CopyID:     copyID,
		BorrowedAt: now,
		DueAt:      now.Add(period),
		Status:     LoanStatusActive,
	}, nil
}

// IsReturned reports whether the copy came back.
func (l *Loan) IsReturned() bool {
	return l.ReturnedAt != nil
}

// IsOverdue reports whether the copy is still out past its due date.
func (l *Loan) IsOverdue(now time.Time) bool {
	return !l.IsReturned() && now.UTC().After(l.DueAt)
}

// EffectiveStatus is the status a client should see: RETURNED once the copy is
// back, OVERDUE while it is out past the due date, ACTIVE otherwise.
func (l *Loan) EffectiveStatus(now time.Time) EffectiveStatus {
	switch {
	case l.IsReturned():
		return StatusReturned
	case l.IsOverdue(now):
		return StatusOverdue
	default:
		return StatusActive
	}
}

// Return closes an open loan. Returning an already returned loan is an error:
// the second call would silently rewrite the return timestamp of the first.
func (l *Loan) Return(now time.Time) error {
	if l.IsReturned() {
		return fmt.Errorf("%w: loan %s", ErrAlreadyReturned, l.ID)
	}

	returned := now.UTC()
	l.ReturnedAt = &returned
	l.Status = LoanStatusReturned

	return nil
}

// Renew pushes the due date forward. Only an open loan can be renewed, and the
// new due date may not move past MaxLoanPeriod from now: otherwise a chain of
// renewals would keep a copy out indefinitely.
func (l *Loan) Renew(now time.Time, extension time.Duration) error {
	if l.IsReturned() {
		return fmt.Errorf("%w: loan %s", ErrAlreadyReturned, l.ID)
	}

	if extension < MinLoanPeriod || extension > MaxLoanPeriod {
		return fmt.Errorf("%w: %s, expected %s..%s", ErrInvalidExtension, extension, MinLoanPeriod, MaxLoanPeriod)
	}

	now = now.UTC()

	newDue := l.DueAt.Add(extension)
	if newDue.After(now.Add(MaxLoanPeriod)) {
		return fmt.Errorf("%w: due date would be %s, at most %s from now", ErrInvalidExtension, newDue, MaxLoanPeriod)
	}

	l.DueAt = newDue

	return nil
}

// LoanFilter narrows down a listing. A nil reader or book means "any".
type LoanFilter struct {
	ReaderID *uuid.UUID
	BookID   *uuid.UUID
	// Status restricts the answer to one effective status, empty means any.
	// It is the derived status, so OVERDUE selects open loans past due_at.
	Status EffectiveStatus
	Limit  int
	Offset int
}

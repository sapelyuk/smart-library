// Package service implements the use cases of the loan service on top of the
// repository contract. It owns the rules that span the loan and the copy
// inventory of book-service: a loan is only recorded once a copy has been
// reserved, and a copy is only released once the loan is closed.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/sapelyuk/smart-library/services/loan-service/internal/domain"
	"github.com/sapelyuk/smart-library/services/loan-service/internal/events"
	"github.com/sapelyuk/smart-library/services/loan-service/internal/repository"
)

// Listing limits applied to a listing request.
const (
	DefaultListLimit = 20
	MaxListLimit     = 100
)

// BookClient reserves and releases copies in book-service. *bookclient.Client is
// the implementation; the interface keeps the service testable without a running
// book-service.
type BookClient interface {
	// BorrowCopy reserves the first available copy of the book and returns its id.
	BorrowCopy(ctx context.Context, bookID uuid.UUID) (uuid.UUID, error)

	// ReturnCopy marks the copy available again.
	ReturnCopy(ctx context.Context, copyID uuid.UUID) error
}

// Config tunes the service.
type Config struct {
	// LoanPeriod is the lending period of a new loan; zero means
	// domain.DefaultLoanPeriod.
	LoanPeriod time.Duration

	// Now is the clock; nil means time.Now. Tests inject a fixed clock.
	Now func() time.Time

	// Logger receives the events that could not be published and other warnings;
	// nil means slog.Default().
	Logger *slog.Logger
}

// Service orchestrates the loan repository, the book inventory and the event
// publisher.
type Service struct {
	loans  repository.LoanRepository
	books  BookClient
	events events.Publisher
	period time.Duration
	now    func() time.Time
	log    *slog.Logger
}

// New wires the service with its dependencies.
func New(loans repository.LoanRepository, books BookClient, publisher events.Publisher, cfg Config) (*Service, error) {
	if loans == nil {
		return nil, errors.New("service: loan repository is required")
	}

	if books == nil {
		return nil, errors.New("service: book client is required")
	}

	if publisher == nil {
		return nil, errors.New("service: event publisher is required")
	}

	period := cfg.LoanPeriod
	if period == 0 {
		period = domain.DefaultLoanPeriod
	}

	if period < domain.MinLoanPeriod || period > domain.MaxLoanPeriod {
		return nil, fmt.Errorf("service: loan period %s is out of range", period)
	}

	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}

	return &Service{
		loans:  loans,
		books:  books,
		events: publisher,
		period: period,
		now:    now,
		log:    log,
	}, nil
}

// BorrowInput is the raw input of the Borrow use case.
type BorrowInput struct {
	ReaderID uuid.UUID
	BookID   uuid.UUID
}

// Borrow reserves a copy of the book and records the loan.
//
// The copy is reserved first: a loan without a reserved copy would let the same
// physical item be handed to two readers. If recording the loan fails, the
// reservation is released so the inventory does not leak a copy.
func (s *Service) Borrow(ctx context.Context, caller domain.Principal, input BorrowInput) (*domain.Loan, error) {
	if err := caller.RequireBorrowAccess(input.ReaderID); err != nil {
		return nil, err
	}

	copyID, err := s.books.BorrowCopy(ctx, input.BookID)
	if err != nil {
		return nil, err
	}

	loan, err := domain.NewLoan(input.ReaderID, input.BookID, copyID, s.now(), s.period)
	if err != nil {
		s.releaseCopy(ctx, copyID)

		return nil, err
	}

	if err := s.loans.Create(ctx, loan); err != nil {
		s.releaseCopy(ctx, copyID)

		return nil, fmt.Errorf("borrow: %w", err)
	}

	s.publish(ctx, events.TypeLoanIssued, loan)

	return loan, nil
}

// Return closes a loan and releases the copy in book-service.
func (s *Service) Return(ctx context.Context, caller domain.Principal, loanID uuid.UUID) (*domain.Loan, error) {
	loan, err := s.loans.GetByID(ctx, loanID)
	if err != nil {
		return nil, err
	}

	if err := caller.RequireLoanAccess(loan.ReaderID, "returning a loan"); err != nil {
		return nil, err
	}

	if loan.IsReturned() {
		return nil, fmt.Errorf("%w: loan %s", domain.ErrAlreadyReturned, loan.ID)
	}

	// Release the inventory first: a closed loan whose copy stays ON_LOAN would
	// make the book look unavailable forever.
	if err := s.books.ReturnCopy(ctx, loan.CopyID); err != nil {
		return nil, err
	}

	if err := loan.Return(s.now()); err != nil {
		return nil, err
	}

	if err := s.loans.Update(ctx, loan); err != nil {
		return nil, fmt.Errorf("return: %w", err)
	}

	s.publish(ctx, events.TypeLoanReturned, loan)

	return loan, nil
}

// Renew pushes the due date of an open loan forward.
func (s *Service) Renew(ctx context.Context, caller domain.Principal, loanID uuid.UUID, extension time.Duration) (*domain.Loan, error) {
	loan, err := s.loans.GetByID(ctx, loanID)
	if err != nil {
		return nil, err
	}

	if err := caller.RequireLoanAccess(loan.ReaderID, "renewing a loan"); err != nil {
		return nil, err
	}

	if err := loan.Renew(s.now(), extension); err != nil {
		return nil, err
	}

	if err := s.loans.Update(ctx, loan); err != nil {
		return nil, fmt.Errorf("renew: %w", err)
	}

	return loan, nil
}

// Get returns one loan the caller is allowed to see.
func (s *Service) Get(ctx context.Context, caller domain.Principal, loanID uuid.UUID) (*domain.Loan, error) {
	loan, err := s.loans.GetByID(ctx, loanID)
	if err != nil {
		return nil, err
	}

	if err := caller.RequireLoanAccess(loan.ReaderID, "reading a loan"); err != nil {
		return nil, err
	}

	return loan, nil
}

// List returns a page of loans. A reader only ever sees own loans, whatever the
// filter asks for; a librarian sees every loan.
func (s *Service) List(ctx context.Context, caller domain.Principal, filter domain.LoanFilter) ([]*domain.Loan, int, error) {
	filter = s.scopeFilter(caller, filter)

	loans, total, err := s.loans.List(ctx, filter)
	if err != nil {
		return nil, 0, fmt.Errorf("list loans: %w", err)
	}

	return loans, total, nil
}

// ListOverdue returns a page of open loans past their due date, scoped to the
// caller the same way List is.
func (s *Service) ListOverdue(ctx context.Context, caller domain.Principal, limit, offset int) ([]*domain.Loan, int, error) {
	filter := domain.LoanFilter{Status: domain.StatusOverdue, Limit: limit, Offset: offset}
	filter = s.scopeFilter(caller, filter)

	loans, total, err := s.loans.List(ctx, filter)
	if err != nil {
		return nil, 0, fmt.Errorf("list overdue loans: %w", err)
	}

	return loans, total, nil
}

// scopeFilter applies the caller's visibility and the listing limits. A reader
// is pinned to own loans; a librarian keeps whatever the caller asked for.
func (s *Service) scopeFilter(caller domain.Principal, filter domain.LoanFilter) domain.LoanFilter {
	if !caller.IsLibrarian() {
		own := caller.UserID
		filter.ReaderID = &own
	}

	filter.Limit = normalizeLimit(filter.Limit)

	if filter.Offset < 0 {
		filter.Offset = 0
	}

	return filter
}

// releaseCopy returns a reserved copy to the inventory after the loan could not
// be recorded. The failure is logged and swallowed: the caller already has an
// error to report, and a leaked copy is better than a masked root cause.
func (s *Service) releaseCopy(ctx context.Context, copyID uuid.UUID) {
	if err := s.books.ReturnCopy(ctx, copyID); err != nil {
		s.log.ErrorContext(ctx, "cannot release a reserved copy", "copy_id", copyID, "error", err)
	}
}

// publish sends a domain event. Publishing is best effort: the loan is already
// recorded, and a broker outage must not turn a successful borrow into an error.
func (s *Service) publish(ctx context.Context, eventType string, loan *domain.Loan) {
	if err := s.events.Publish(ctx, eventType, loanPayload(loan)); err != nil {
		s.log.ErrorContext(ctx, "cannot publish a domain event", "event_type", eventType, "loan_id", loan.ID, "error", err)
	}
}

// loanPayload is the wire shape of a loan inside an event. It mirrors the
// ADR-0001 envelope payload: flat, self-contained, no internal types.
func loanPayload(loan *domain.Loan) map[string]any {
	payload := map[string]any{
		"loan_id":     loan.ID.String(),
		"reader_id":   loan.ReaderID.String(),
		"book_id":     loan.BookID.String(),
		"copy_id":     loan.CopyID.String(),
		"borrowed_at": loan.BorrowedAt,
		"due_at":      loan.DueAt,
	}

	if loan.ReturnedAt != nil {
		payload["returned_at"] = *loan.ReturnedAt
	}

	return payload
}

func normalizeLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultListLimit
	case limit > MaxListLimit:
		return MaxListLimit
	default:
		return limit
	}
}

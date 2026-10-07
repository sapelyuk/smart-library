package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sapelyuk/smart-library/services/loan-service/internal/domain"
)

var (
	readerID = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	bookID   = uuid.MustParse("22222222-2222-2222-2222-222222222222")
	copyID   = uuid.MustParse("33333333-3333-3333-3333-333333333333")
)

// fixedNow is a stable clock for the whole test file.
var fixedNow = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func TestNewLoan(t *testing.T) {
	t.Parallel()

	loan, err := domain.NewLoan(readerID, bookID, copyID, fixedNow, domain.DefaultLoanPeriod)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if loan.ID == uuid.Nil {
		t.Fatal("expected a generated id")
	}

	if loan.Status != domain.LoanStatusActive {
		t.Fatalf("status = %q, want ACTIVE", loan.Status)
	}

	if !loan.BorrowedAt.Equal(fixedNow) {
		t.Fatalf("borrowed_at = %s, want %s", loan.BorrowedAt, fixedNow)
	}

	if want := fixedNow.Add(domain.DefaultLoanPeriod); !loan.DueAt.Equal(want) {
		t.Fatalf("due_at = %s, want %s", loan.DueAt, want)
	}

	if loan.IsReturned() {
		t.Fatal("a new loan must not be returned")
	}
}

func TestNewLoanValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		reader  uuid.UUID
		book    uuid.UUID
		copy    uuid.UUID
		period  time.Duration
		wantErr error
	}{
		{
			name:    "empty reader",
			reader:  uuid.Nil,
			book:    bookID,
			copy:    copyID,
			period:  domain.DefaultLoanPeriod,
			wantErr: domain.ErrInvalidReaderID,
		},
		{
			name:    "empty book",
			reader:  readerID,
			book:    uuid.Nil,
			copy:    copyID,
			period:  domain.DefaultLoanPeriod,
			wantErr: domain.ErrInvalidBookID,
		},
		{
			name:    "empty copy",
			reader:  readerID,
			book:    bookID,
			copy:    uuid.Nil,
			period:  domain.DefaultLoanPeriod,
			wantErr: domain.ErrInvalidCopyID,
		},
		{
			name:    "period too short",
			reader:  readerID,
			book:    bookID,
			copy:    copyID,
			period:  time.Hour,
			wantErr: domain.ErrInvalidLoanPeriod,
		},
		{
			name:    "period too long",
			reader:  readerID,
			book:    bookID,
			copy:    copyID,
			period:  domain.MaxLoanPeriod + time.Hour,
			wantErr: domain.ErrInvalidLoanPeriod,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := domain.NewLoan(tc.reader, tc.book, tc.copy, fixedNow, tc.period)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestLoanEffectiveStatus(t *testing.T) {
	t.Parallel()

	loan, err := domain.NewLoan(readerID, bookID, copyID, fixedNow, domain.DefaultLoanPeriod)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Before the due date the loan is active.
	if got := loan.EffectiveStatus(fixedNow.Add(24 * time.Hour)); got != domain.StatusActive {
		t.Errorf("status before due = %q, want ACTIVE", got)
	}

	// After the due date, while still out, it is overdue.
	if got := loan.EffectiveStatus(loan.DueAt.Add(time.Hour)); got != domain.StatusOverdue {
		t.Errorf("status after due = %q, want OVERDUE", got)
	}

	// Once returned, the overdue state no longer applies.
	if err := loan.Return(loan.DueAt.Add(2 * time.Hour)); err != nil {
		t.Fatalf("return: %v", err)
	}

	if got := loan.EffectiveStatus(loan.DueAt.Add(48 * time.Hour)); got != domain.StatusReturned {
		t.Errorf("status after return = %q, want RETURNED", got)
	}
}

func TestLoanReturn(t *testing.T) {
	t.Parallel()

	loan, err := domain.NewLoan(readerID, bookID, copyID, fixedNow, domain.DefaultLoanPeriod)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	returnedAt := fixedNow.Add(72 * time.Hour)

	if err := loan.Return(returnedAt); err != nil {
		t.Fatalf("return: %v", err)
	}

	if !loan.IsReturned() {
		t.Fatal("loan must be returned")
	}

	if loan.Status != domain.LoanStatusReturned {
		t.Errorf("status = %q, want RETURNED", loan.Status)
	}

	if loan.ReturnedAt == nil || !loan.ReturnedAt.Equal(returnedAt) {
		t.Errorf("returned_at = %v, want %s", loan.ReturnedAt, returnedAt)
	}

	// Returning twice must not silently rewrite the first timestamp.
	err = loan.Return(fixedNow.Add(96 * time.Hour))
	if !errors.Is(err, domain.ErrAlreadyReturned) {
		t.Fatalf("second return error = %v, want ErrAlreadyReturned", err)
	}

	if !loan.ReturnedAt.Equal(returnedAt) {
		t.Errorf("returned_at changed on the second return: %s", loan.ReturnedAt)
	}
}

func TestLoanRenew(t *testing.T) {
	t.Parallel()

	loan, err := domain.NewLoan(readerID, bookID, copyID, fixedNow, domain.DefaultLoanPeriod)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	originalDue := loan.DueAt

	if err := loan.Renew(fixedNow, 7*24*time.Hour); err != nil {
		t.Fatalf("renew: %v", err)
	}

	if want := originalDue.Add(7 * 24 * time.Hour); !loan.DueAt.Equal(want) {
		t.Errorf("due_at = %s, want %s", loan.DueAt, want)
	}

	if loan.Status != domain.LoanStatusActive {
		t.Errorf("renewal changed the status to %q", loan.Status)
	}
}

func TestLoanRenewValidation(t *testing.T) {
	t.Parallel()

	t.Run("invalid extension", func(t *testing.T) {
		t.Parallel()

		loan, err := domain.NewLoan(readerID, bookID, copyID, fixedNow, domain.DefaultLoanPeriod)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if err := loan.Renew(fixedNow, time.Hour); !errors.Is(err, domain.ErrInvalidExtension) {
			t.Fatalf("error = %v, want ErrInvalidExtension", err)
		}
	})

	t.Run("beyond the maximum", func(t *testing.T) {
		t.Parallel()

		loan, err := domain.NewLoan(readerID, bookID, copyID, fixedNow, domain.MaxLoanPeriod)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Extending a loan that already runs for the maximum period would push
		// the due date past now + MaxLoanPeriod.
		if err := loan.Renew(fixedNow, domain.MaxLoanPeriod); !errors.Is(err, domain.ErrInvalidExtension) {
			t.Fatalf("error = %v, want ErrInvalidExtension", err)
		}
	})

	t.Run("returned loan", func(t *testing.T) {
		t.Parallel()

		loan, err := domain.NewLoan(readerID, bookID, copyID, fixedNow, domain.DefaultLoanPeriod)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if err := loan.Return(fixedNow.Add(time.Hour)); err != nil {
			t.Fatalf("return: %v", err)
		}

		if err := loan.Renew(fixedNow.Add(2*time.Hour), 7*24*time.Hour); !errors.Is(err, domain.ErrAlreadyReturned) {
			t.Fatalf("error = %v, want ErrAlreadyReturned", err)
		}
	})
}

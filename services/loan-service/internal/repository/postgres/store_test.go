package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sapelyuk/smart-library/services/loan-service/internal/domain"
	"github.com/sapelyuk/smart-library/services/loan-service/internal/repository"
	"github.com/sapelyuk/smart-library/services/loan-service/internal/repository/postgres"
	"github.com/sapelyuk/smart-library/services/loan-service/internal/testdb"
)

var _ repository.LoanRepository = (*postgres.Store)(nil)

// fixedNow keeps the timestamps of the fixtures stable across the assertions.
var fixedNow = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

// newStore returns a store over a freshly truncated PostgreSQL database.
func newStore(t *testing.T) *postgres.Store {
	t.Helper()

	return postgres.NewStore(testdb.New(t))
}

// newStoreWithDB returns both the store and the underlying pool, for the tests
// that need to move a row outside the domain rules.
func newStoreWithDB(t *testing.T) (*postgres.Store, *sql.DB) {
	t.Helper()

	db := testdb.New(t)

	return postgres.NewStore(db), db
}

// newLoan builds a valid open loan for the tests.
func newLoan(t *testing.T, readerID, bookID, copyID uuid.UUID) *domain.Loan {
	t.Helper()

	loan, err := domain.NewLoan(readerID, bookID, copyID, fixedNow, domain.DefaultLoanPeriod)
	if err != nil {
		t.Fatalf("NewLoan: unexpected error: %v", err)
	}

	return loan
}

func TestCreateAndGetLoan(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	loan := newLoan(t, uuid.New(), uuid.New(), uuid.New())

	if err := store.Create(ctx, loan); err != nil {
		t.Fatalf("Create: unexpected error: %v", err)
	}

	fetched, err := store.GetByID(ctx, loan.ID)
	if err != nil {
		t.Fatalf("GetByID: unexpected error: %v", err)
	}

	if fetched.ReaderID != loan.ReaderID || fetched.BookID != loan.BookID || fetched.CopyID != loan.CopyID {
		t.Fatalf("round trip lost identifiers: %+v", fetched)
	}

	if fetched.Status != domain.LoanStatusActive {
		t.Errorf("status = %q, want ACTIVE", fetched.Status)
	}

	if fetched.ReturnedAt != nil {
		t.Errorf("returned_at = %v, want nil for an open loan", fetched.ReturnedAt)
	}

	if !fetched.DueAt.Equal(loan.DueAt) {
		t.Errorf("due_at = %s, want %s", fetched.DueAt, loan.DueAt)
	}
}

func TestGetLoanUnknownID(t *testing.T) {
	store := newStore(t)

	if _, err := store.GetByID(context.Background(), uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want %v, got %v", domain.ErrNotFound, err)
	}
}

// TestCreateRejectsSecondActiveLoanOfCopy proves the partial unique index is
// what stops a physical copy from being lent twice, not a check in the service.
func TestCreateRejectsSecondActiveLoanOfCopy(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	copyID := uuid.New()

	if err := store.Create(ctx, newLoan(t, uuid.New(), uuid.New(), copyID)); err != nil {
		t.Fatalf("first Create: unexpected error: %v", err)
	}

	err := store.Create(ctx, newLoan(t, uuid.New(), uuid.New(), copyID))
	if !errors.Is(err, domain.ErrCopyAlreadyOnLoan) {
		t.Fatalf("want %v, got %v", domain.ErrCopyAlreadyOnLoan, err)
	}
}

// TestCopyCanBeLentAgainAfterReturn proves the uniqueness constraint is partial:
// the lending history of a copy stays in the table.
func TestCopyCanBeLentAgainAfterReturn(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	copyID := uuid.New()

	first := newLoan(t, uuid.New(), uuid.New(), copyID)

	if err := store.Create(ctx, first); err != nil {
		t.Fatalf("first Create: unexpected error: %v", err)
	}

	if err := first.Return(fixedNow.Add(time.Hour)); err != nil {
		t.Fatalf("Return: unexpected error: %v", err)
	}

	if err := store.Update(ctx, first); err != nil {
		t.Fatalf("Update: unexpected error: %v", err)
	}

	second := newLoan(t, uuid.New(), uuid.New(), copyID)

	if err := store.Create(ctx, second); err != nil {
		t.Fatalf("second Create after return: unexpected error: %v", err)
	}

	fetched, err := store.GetByID(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetByID: unexpected error: %v", err)
	}

	if fetched.Status != domain.LoanStatusReturned || fetched.ReturnedAt == nil {
		t.Fatalf("first loan must stay returned: %+v", fetched)
	}
}

func TestUpdatePersistsReturn(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	loan := newLoan(t, uuid.New(), uuid.New(), uuid.New())

	if err := store.Create(ctx, loan); err != nil {
		t.Fatalf("Create: unexpected error: %v", err)
	}

	returnedAt := fixedNow.Add(48 * time.Hour)

	if err := loan.Return(returnedAt); err != nil {
		t.Fatalf("Return: unexpected error: %v", err)
	}

	if err := store.Update(ctx, loan); err != nil {
		t.Fatalf("Update: unexpected error: %v", err)
	}

	fetched, err := store.GetByID(ctx, loan.ID)
	if err != nil {
		t.Fatalf("GetByID: unexpected error: %v", err)
	}

	if fetched.Status != domain.LoanStatusReturned {
		t.Errorf("status = %q, want RETURNED", fetched.Status)
	}

	if fetched.ReturnedAt == nil || !fetched.ReturnedAt.Equal(returnedAt) {
		t.Errorf("returned_at = %v, want %s", fetched.ReturnedAt, returnedAt)
	}
}

func TestUpdatePersistsRenewal(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	loan := newLoan(t, uuid.New(), uuid.New(), uuid.New())

	if err := store.Create(ctx, loan); err != nil {
		t.Fatalf("Create: unexpected error: %v", err)
	}

	if err := loan.Renew(fixedNow, 7*24*time.Hour); err != nil {
		t.Fatalf("Renew: unexpected error: %v", err)
	}

	if err := store.Update(ctx, loan); err != nil {
		t.Fatalf("Update: unexpected error: %v", err)
	}

	fetched, err := store.GetByID(ctx, loan.ID)
	if err != nil {
		t.Fatalf("GetByID: unexpected error: %v", err)
	}

	if !fetched.DueAt.Equal(loan.DueAt) {
		t.Errorf("due_at = %s, want %s", fetched.DueAt, loan.DueAt)
	}
}

func TestUpdateUnknownLoan(t *testing.T) {
	store := newStore(t)

	err := store.Update(context.Background(), newLoan(t, uuid.New(), uuid.New(), uuid.New()))
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want %v, got %v", domain.ErrNotFound, err)
	}
}

func TestListFiltersByReaderAndBook(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	reader := uuid.New()
	other := uuid.New()
	book := uuid.New()

	seed := []*domain.Loan{
		newLoan(t, reader, book, uuid.New()),
		newLoan(t, reader, uuid.New(), uuid.New()),
		newLoan(t, other, book, uuid.New()),
	}

	for _, loan := range seed {
		if err := store.Create(ctx, loan); err != nil {
			t.Fatalf("Create: unexpected error: %v", err)
		}
	}

	loans, total, err := store.List(ctx, domain.LoanFilter{ReaderID: &reader, Limit: 10})
	if err != nil {
		t.Fatalf("List by reader: unexpected error: %v", err)
	}

	if total != 2 || len(loans) != 2 {
		t.Fatalf("reader filter returned %d loans (total %d), want 2", len(loans), total)
	}

	loans, total, err = store.List(ctx, domain.LoanFilter{BookID: &book, Limit: 10})
	if err != nil {
		t.Fatalf("List by book: unexpected error: %v", err)
	}

	if total != 2 || len(loans) != 2 {
		t.Fatalf("book filter returned %d loans (total %d), want 2", len(loans), total)
	}

	loans, total, err = store.List(ctx, domain.LoanFilter{ReaderID: &reader, BookID: &book, Limit: 10})
	if err != nil {
		t.Fatalf("List by reader and book: unexpected error: %v", err)
	}

	if total != 1 || len(loans) != 1 {
		t.Fatalf("combined filter returned %d loans (total %d), want 1", len(loans), total)
	}
}

func TestListFiltersByStatus(t *testing.T) {
	ctx := context.Background()
	store, db := newStoreWithDB(t)

	// One open loan that is not due yet, one open loan already past due, one
	// returned loan.
	active := newLoan(t, uuid.New(), uuid.New(), uuid.New())

	if err := store.Create(ctx, active); err != nil {
		t.Fatalf("Create active: unexpected error: %v", err)
	}

	overdue := newLoan(t, uuid.New(), uuid.New(), uuid.New())

	if err := store.Create(ctx, overdue); err != nil {
		t.Fatalf("Create overdue: unexpected error: %v", err)
	}

	// Move the whole lending window into the past directly: the domain refuses
	// to build a loan whose due date is already behind us.
	if _, err := db.ExecContext(ctx,
		`UPDATE loans SET borrowed_at = now() - interval '10 days', due_at = now() - interval '1 day' WHERE id = $1`, overdue.ID,
	); err != nil {
		t.Fatalf("age the loan: %v", err)
	}

	returned := newLoan(t, uuid.New(), uuid.New(), uuid.New())

	if err := store.Create(ctx, returned); err != nil {
		t.Fatalf("Create returned: unexpected error: %v", err)
	}

	if err := returned.Return(fixedNow.Add(time.Hour)); err != nil {
		t.Fatalf("Return: unexpected error: %v", err)
	}

	if err := store.Update(ctx, returned); err != nil {
		t.Fatalf("Update: unexpected error: %v", err)
	}

	activeList, activeTotal, err := store.List(ctx, domain.LoanFilter{Status: domain.StatusActive, Limit: 10})
	if err != nil {
		t.Fatalf("List active: unexpected error: %v", err)
	}

	if activeTotal != 1 || len(activeList) != 1 || activeList[0].ID != active.ID {
		t.Fatalf("active filter returned %d loans (total %d), want the not-yet-due one", len(activeList), activeTotal)
	}

	overdueList, overdueTotal, err := store.List(ctx, domain.LoanFilter{Status: domain.StatusOverdue, Limit: 10})
	if err != nil {
		t.Fatalf("List overdue: unexpected error: %v", err)
	}

	if overdueTotal != 1 || len(overdueList) != 1 || overdueList[0].ID != overdue.ID {
		t.Fatalf("overdue filter returned %d loans (total %d), want the past-due one", len(overdueList), overdueTotal)
	}

	returnedList, returnedTotal, err := store.List(ctx, domain.LoanFilter{Status: domain.StatusReturned, Limit: 10})
	if err != nil {
		t.Fatalf("List returned: unexpected error: %v", err)
	}

	if returnedTotal != 1 || len(returnedList) != 1 || returnedList[0].ID != returned.ID {
		t.Fatalf("returned filter returned %d loans (total %d), want the closed one", len(returnedList), returnedTotal)
	}
}

func TestListPagination(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	reader := uuid.New()

	for i := 0; i < 5; i++ {
		loan := newLoan(t, reader, uuid.New(), uuid.New())
		loan.BorrowedAt = fixedNow.Add(time.Duration(i) * time.Hour)

		if err := store.Create(ctx, loan); err != nil {
			t.Fatalf("Create: unexpected error: %v", err)
		}
	}

	firstPage, total, err := store.List(ctx, domain.LoanFilter{ReaderID: &reader, Limit: 2, Offset: 0})
	if err != nil {
		t.Fatalf("first page: unexpected error: %v", err)
	}

	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}

	if len(firstPage) != 2 {
		t.Fatalf("first page returned %d loans, want 2", len(firstPage))
	}

	secondPage, _, err := store.List(ctx, domain.LoanFilter{ReaderID: &reader, Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("second page: unexpected error: %v", err)
	}

	if len(secondPage) != 2 {
		t.Fatalf("second page returned %d loans, want 2", len(secondPage))
	}

	// Newest first: the first page holds the two most recent loans.
	if !firstPage[0].BorrowedAt.After(firstPage[1].BorrowedAt) {
		t.Errorf("listing is not ordered by borrowed_at DESC")
	}

	seen := map[uuid.UUID]bool{}
	for _, loan := range append(firstPage, secondPage...) {
		if seen[loan.ID] {
			t.Errorf("loan %s appears on both pages", loan.ID)
		}

		seen[loan.ID] = true
	}
}

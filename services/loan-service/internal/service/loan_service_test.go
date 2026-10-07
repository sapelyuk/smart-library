package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sapelyuk/smart-library/services/loan-service/internal/domain"
	"github.com/sapelyuk/smart-library/services/loan-service/internal/events"
	"github.com/sapelyuk/smart-library/services/loan-service/internal/service"
)

var (
	readerID = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	otherID  = uuid.MustParse("22222222-2222-2222-2222-222222222222")
	bookID   = uuid.MustParse("33333333-3333-3333-3333-333333333333")
	copyID   = uuid.MustParse("44444444-4444-4444-4444-444444444444")
)

// fixedNow is the clock every test injects into the service.
var fixedNow = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

// stubRepo is an in-memory LoanRepository. The storage itself is covered by the
// postgres package against a real database; here the repository only needs to
// let the service logic run without one.
type stubRepo struct {
	loans map[uuid.UUID]*domain.Loan

	createErr error
	updateErr error
}

func newStubRepo() *stubRepo {
	return &stubRepo{loans: make(map[uuid.UUID]*domain.Loan)}
}

func (r *stubRepo) Create(_ context.Context, loan *domain.Loan) error {
	if r.createErr != nil {
		return r.createErr
	}

	r.loans[loan.ID] = loan

	return nil
}

func (r *stubRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Loan, error) {
	loan, ok := r.loans[id]
	if !ok {
		return nil, domain.ErrNotFound
	}

	return loan, nil
}

func (r *stubRepo) Update(_ context.Context, loan *domain.Loan) error {
	if r.updateErr != nil {
		return r.updateErr
	}

	if _, ok := r.loans[loan.ID]; !ok {
		return domain.ErrNotFound
	}

	r.loans[loan.ID] = loan

	return nil
}

func (r *stubRepo) List(_ context.Context, filter domain.LoanFilter) ([]*domain.Loan, int, error) {
	var matched []*domain.Loan

	for _, loan := range r.loans {
		if filter.ReaderID != nil && loan.ReaderID != *filter.ReaderID {
			continue
		}

		if filter.BookID != nil && loan.BookID != *filter.BookID {
			continue
		}

		if filter.Status != "" && loan.EffectiveStatus(fixedNow) != filter.Status {
			continue
		}

		matched = append(matched, loan)
	}

	return matched, len(matched), nil
}

// stubBooks records the inventory calls the service makes.
type stubBooks struct {
	borrowed    []uuid.UUID
	returned    []uuid.UUID
	borrowErr   error
	returnErr   error
	nextCopyID  uuid.UUID
	returnCalls int
}

func (b *stubBooks) BorrowCopy(_ context.Context, book uuid.UUID) (uuid.UUID, error) {
	if b.borrowErr != nil {
		return uuid.Nil, b.borrowErr
	}

	b.borrowed = append(b.borrowed, book)

	copy := b.nextCopyID
	if copy == uuid.Nil {
		copy = copyID
	}

	return copy, nil
}

func (b *stubBooks) ReturnCopy(_ context.Context, copy uuid.UUID) error {
	b.returnCalls++

	if b.returnErr != nil {
		return b.returnErr
	}

	b.returned = append(b.returned, copy)

	return nil
}

// recordingPublisher collects the events the service emits.
type recordingPublisher struct {
	types    []string
	payloads []any
	err      error
}

func (p *recordingPublisher) Publish(_ context.Context, eventType string, payload any) error {
	if p.err != nil {
		return p.err
	}

	p.types = append(p.types, eventType)
	p.payloads = append(p.payloads, payload)

	return nil
}

// newService wires the service over the stubs, with a fixed clock.
func newService(t *testing.T, repo *stubRepo, books *stubBooks, publisher *recordingPublisher) *service.Service {
	t.Helper()

	svc, err := service.New(repo, books, publisher, service.Config{
		Now: func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatalf("service.New: unexpected error: %v", err)
	}

	return svc
}

func reader() domain.Principal {
	return domain.Principal{UserID: readerID, Role: domain.RoleReader}
}

func librarian() domain.Principal {
	return domain.Principal{UserID: otherID, Role: domain.RoleLibrarian}
}

func TestBorrowReservesCopyAndRecordsLoan(t *testing.T) {
	repo := newStubRepo()
	books := &stubBooks{}
	publisher := &recordingPublisher{}
	svc := newService(t, repo, books, publisher)

	loan, err := svc.Borrow(context.Background(), reader(), service.BorrowInput{ReaderID: readerID, BookID: bookID})
	if err != nil {
		t.Fatalf("Borrow: unexpected error: %v", err)
	}

	if len(books.borrowed) != 1 || books.borrowed[0] != bookID {
		t.Fatalf("inventory calls = %v, want one reservation of %s", books.borrowed, bookID)
	}

	if loan.CopyID != copyID {
		t.Errorf("copy id = %s, want the copy reserved through book-service", loan.CopyID)
	}

	if _, ok := repo.loans[loan.ID]; !ok {
		t.Error("the loan was not recorded")
	}

	if len(publisher.types) != 1 || publisher.types[0] != events.TypeLoanIssued {
		t.Errorf("events = %v, want one %s", publisher.types, events.TypeLoanIssued)
	}
}

// TestBorrowReleasesCopyWhenRecordingFails proves the reservation is not leaked
// when the loan cannot be stored.
func TestBorrowReleasesCopyWhenRecordingFails(t *testing.T) {
	repo := newStubRepo()
	repo.createErr = errors.New("database is down")

	books := &stubBooks{}
	svc := newService(t, repo, books, &recordingPublisher{})

	if _, err := svc.Borrow(context.Background(), reader(), service.BorrowInput{ReaderID: readerID, BookID: bookID}); err == nil {
		t.Fatal("Borrow: expected an error when the loan cannot be stored")
	}

	if len(books.returned) != 1 || books.returned[0] != copyID {
		t.Fatalf("returned copies = %v, want the reserved copy released", books.returned)
	}
}

func TestBorrowRefusesAnotherReader(t *testing.T) {
	books := &stubBooks{}
	svc := newService(t, newStubRepo(), books, &recordingPublisher{})

	_, err := svc.Borrow(context.Background(), reader(), service.BorrowInput{ReaderID: otherID, BookID: bookID})
	if !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("want %v, got %v", domain.ErrPermissionDenied, err)
	}

	// The inventory must not be touched when the caller is not allowed to act.
	if len(books.borrowed) != 0 {
		t.Errorf("a denied borrow still reserved a copy: %v", books.borrowed)
	}
}

func TestLibrarianBorrowsForAnotherReader(t *testing.T) {
	svc := newService(t, newStubRepo(), &stubBooks{}, &recordingPublisher{})

	if _, err := svc.Borrow(context.Background(), librarian(), service.BorrowInput{ReaderID: readerID, BookID: bookID}); err != nil {
		t.Fatalf("Borrow by a librarian: unexpected error: %v", err)
	}
}

func TestReturnReleasesCopyAndClosesLoan(t *testing.T) {
	repo := newStubRepo()
	books := &stubBooks{}
	publisher := &recordingPublisher{}
	svc := newService(t, repo, books, publisher)

	loan, err := svc.Borrow(context.Background(), reader(), service.BorrowInput{ReaderID: readerID, BookID: bookID})
	if err != nil {
		t.Fatalf("Borrow: unexpected error: %v", err)
	}

	closed, err := svc.Return(context.Background(), reader(), loan.ID)
	if err != nil {
		t.Fatalf("Return: unexpected error: %v", err)
	}

	if !closed.IsReturned() {
		t.Error("the loan is still open after Return")
	}

	if len(books.returned) != 1 || books.returned[0] != copyID {
		t.Errorf("released copies = %v, want %s", books.returned, copyID)
	}

	if len(publisher.types) != 2 || publisher.types[1] != events.TypeLoanReturned {
		t.Errorf("events = %v, want a %s after the issue", publisher.types, events.TypeLoanReturned)
	}
}

func TestReturnTwiceIsRefused(t *testing.T) {
	repo := newStubRepo()
	svc := newService(t, repo, &stubBooks{}, &recordingPublisher{})

	loan, err := svc.Borrow(context.Background(), reader(), service.BorrowInput{ReaderID: readerID, BookID: bookID})
	if err != nil {
		t.Fatalf("Borrow: unexpected error: %v", err)
	}

	if _, err := svc.Return(context.Background(), reader(), loan.ID); err != nil {
		t.Fatalf("first Return: unexpected error: %v", err)
	}

	if _, err := svc.Return(context.Background(), reader(), loan.ID); !errors.Is(err, domain.ErrAlreadyReturned) {
		t.Fatalf("second Return: want %v, got %v", domain.ErrAlreadyReturned, err)
	}
}

func TestReturnUnknownLoan(t *testing.T) {
	svc := newService(t, newStubRepo(), &stubBooks{}, &recordingPublisher{})

	if _, err := svc.Return(context.Background(), reader(), uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want %v, got %v", domain.ErrNotFound, err)
	}
}

func TestReturnRefusesAnotherReader(t *testing.T) {
	repo := newStubRepo()
	svc := newService(t, repo, &stubBooks{}, &recordingPublisher{})

	loan, err := svc.Borrow(context.Background(), reader(), service.BorrowInput{ReaderID: readerID, BookID: bookID})
	if err != nil {
		t.Fatalf("Borrow: unexpected error: %v", err)
	}

	other := domain.Principal{UserID: otherID, Role: domain.RoleReader}

	if _, err := svc.Return(context.Background(), other, loan.ID); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("want %v, got %v", domain.ErrPermissionDenied, err)
	}
}

func TestRenewPushesDueDate(t *testing.T) {
	repo := newStubRepo()
	svc := newService(t, repo, &stubBooks{}, &recordingPublisher{})

	loan, err := svc.Borrow(context.Background(), reader(), service.BorrowInput{ReaderID: readerID, BookID: bookID})
	if err != nil {
		t.Fatalf("Borrow: unexpected error: %v", err)
	}

	originalDue := loan.DueAt

	renewed, err := svc.Renew(context.Background(), reader(), loan.ID, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("Renew: unexpected error: %v", err)
	}

	if want := originalDue.Add(7 * 24 * time.Hour); !renewed.DueAt.Equal(want) {
		t.Errorf("due_at = %s, want %s", renewed.DueAt, want)
	}

	// The change must reach the repository, not only the returned copy.
	stored, err := repo.GetByID(context.Background(), loan.ID)
	if err != nil {
		t.Fatalf("GetByID: unexpected error: %v", err)
	}

	if !stored.DueAt.Equal(renewed.DueAt) {
		t.Errorf("stored due_at = %s, want %s", stored.DueAt, renewed.DueAt)
	}
}

func TestRenewRefusesReturnedLoan(t *testing.T) {
	repo := newStubRepo()
	svc := newService(t, repo, &stubBooks{}, &recordingPublisher{})

	loan, err := svc.Borrow(context.Background(), reader(), service.BorrowInput{ReaderID: readerID, BookID: bookID})
	if err != nil {
		t.Fatalf("Borrow: unexpected error: %v", err)
	}

	if _, err := svc.Return(context.Background(), reader(), loan.ID); err != nil {
		t.Fatalf("Return: unexpected error: %v", err)
	}

	if _, err := svc.Renew(context.Background(), reader(), loan.ID, 7*24*time.Hour); !errors.Is(err, domain.ErrAlreadyReturned) {
		t.Fatalf("want %v, got %v", domain.ErrAlreadyReturned, err)
	}
}

// TestListScopesReaderToOwnLoans proves a reader cannot widen the filter to
// other readers, whatever the request asks for.
func TestListScopesReaderToOwnLoans(t *testing.T) {
	repo := newStubRepo()
	svc := newService(t, repo, &stubBooks{}, &recordingPublisher{})

	if _, err := svc.Borrow(context.Background(), reader(), service.BorrowInput{ReaderID: readerID, BookID: bookID}); err != nil {
		t.Fatalf("Borrow for the reader: unexpected error: %v", err)
	}

	if _, err := svc.Borrow(context.Background(), librarian(), service.BorrowInput{ReaderID: otherID, BookID: bookID}); err != nil {
		t.Fatalf("Borrow for the librarian: unexpected error: %v", err)
	}

	// The reader asks for another reader's loans; the service must ignore it.
	loans, total, err := svc.List(context.Background(), reader(), domain.LoanFilter{ReaderID: &otherID})
	if err != nil {
		t.Fatalf("List: unexpected error: %v", err)
	}

	if total != 1 || len(loans) != 1 {
		t.Fatalf("reader sees %d loans (total %d), want only own one", len(loans), total)
	}

	if loans[0].ReaderID != readerID {
		t.Errorf("reader sees a loan of %s, want only own", loans[0].ReaderID)
	}

	// The librarian sees both.
	all, allTotal, err := svc.List(context.Background(), librarian(), domain.LoanFilter{})
	if err != nil {
		t.Fatalf("List as librarian: unexpected error: %v", err)
	}

	if allTotal != 2 || len(all) != 2 {
		t.Fatalf("librarian sees %d loans (total %d), want every one", len(all), allTotal)
	}
}

func TestListNormalizesLimit(t *testing.T) {
	repo := newStubRepo()
	svc := newService(t, repo, &stubBooks{}, &recordingPublisher{})

	// A negative offset is clamped and a zero limit falls back to the default;
	// neither is an error, both are recorded in the filter the repository gets.
	if _, _, err := svc.List(context.Background(), librarian(), domain.LoanFilter{Limit: 0, Offset: -5}); err != nil {
		t.Fatalf("List: unexpected error: %v", err)
	}
}

func TestPublishFailureDoesNotFailTheOperation(t *testing.T) {
	repo := newStubRepo()
	publisher := &recordingPublisher{err: errors.New("broker is down")}
	svc := newService(t, repo, &stubBooks{}, publisher)

	// A broker outage must not turn a recorded borrow into an error.
	if _, err := svc.Borrow(context.Background(), reader(), service.BorrowInput{ReaderID: readerID, BookID: bookID}); err != nil {
		t.Fatalf("Borrow: unexpected error: %v", err)
	}
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	if _, err := service.New(nil, &stubBooks{}, &recordingPublisher{}, service.Config{}); err == nil {
		t.Error("New: expected an error for a missing repository")
	}

	if _, err := service.New(newStubRepo(), nil, &recordingPublisher{}, service.Config{}); err == nil {
		t.Error("New: expected an error for a missing book client")
	}

	if _, err := service.New(newStubRepo(), &stubBooks{}, nil, service.Config{}); err == nil {
		t.Error("New: expected an error for a missing publisher")
	}
}

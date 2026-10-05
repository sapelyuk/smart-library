package service_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"

	"github.com/google/uuid"

	"github.com/sapelyuk/smart-library/services/book-service/internal/domain"
	"github.com/sapelyuk/smart-library/services/book-service/internal/repository/postgres"
	"github.com/sapelyuk/smart-library/services/book-service/internal/service"
	"github.com/sapelyuk/smart-library/services/book-service/internal/testdb"
)

const (
	isbnA = "978-0-13-419044-0"
	isbnB = "978-0-306-40615-7"
)

// newService wires the service on top of a real PostgreSQL store. The tests are
// not parallel: they share one database and truncate it between cases.
func newService(t *testing.T) *service.BookService {
	t.Helper()

	store := postgres.NewStore(testdb.New(t))

	return service.NewBookService(store, store)
}

func createBook(t *testing.T, svc *service.BookService, isbn, title string) *domain.Book {
	t.Helper()

	book, err := svc.CreateBook(context.Background(), service.CreateBookInput{
		ISBN:          isbn,
		Title:         title,
		Author:        "Author",
		Publisher:     "Publisher",
		PublishedYear: 2020,
	})
	if err != nil {
		t.Fatalf("CreateBook: unexpected error: %v", err)
	}

	return book
}

func TestCreateBookThenGet(t *testing.T) {

	ctx := context.Background()
	svc := newService(t)

	created := createBook(t, svc, isbnA, "The Go Programming Language")

	view, err := svc.GetBook(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetBook: unexpected error: %v", err)
	}

	if view.Book.Title != "The Go Programming Language" {
		t.Fatalf("unexpected title: %q", view.Book.Title)
	}

	// A brand new book has no copies yet.
	if view.Stats.Total != 0 || view.Stats.Available != 0 {
		t.Fatalf("expected empty inventory, got %+v", view.Stats)
	}
}

func TestCreateBookRejectsDuplicateISBN(t *testing.T) {

	ctx := context.Background()
	svc := newService(t)

	createBook(t, svc, isbnA, "First")

	_, err := svc.CreateBook(ctx, service.CreateBookInput{
		ISBN: isbnA, Title: "Second", Author: "Author", PublishedYear: 2021,
	})
	if !errors.Is(err, domain.ErrISBNAlreadyExists) {
		t.Fatalf("want %v, got %v", domain.ErrISBNAlreadyExists, err)
	}
}

func TestGetBookUnknownID(t *testing.T) {

	_, err := newService(t).GetBook(context.Background(), uuid.New())
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want %v, got %v", domain.ErrNotFound, err)
	}
}

func TestInventoryLifecycle(t *testing.T) {

	ctx := context.Background()
	svc := newService(t)

	book := createBook(t, svc, isbnA, "Clean Code")

	first, err := svc.AddBookCopy(ctx, book.ID, "BC-000001")
	if err != nil {
		t.Fatalf("AddBookCopy: unexpected error: %v", err)
	}

	second, err := svc.AddBookCopy(ctx, book.ID, "BC-000002")
	if err != nil {
		t.Fatalf("AddBookCopy: unexpected error: %v", err)
	}

	// A duplicate barcode is rejected even for a different book.
	other := createBook(t, svc, isbnB, "Refactoring")
	if _, err := svc.AddBookCopy(ctx, other.ID, "BC-000001"); !errors.Is(err, domain.ErrBarcodeAlreadyExists) {
		t.Fatalf("want %v, got %v", domain.ErrBarcodeAlreadyExists, err)
	}

	view, err := svc.GetBook(ctx, book.ID)
	if err != nil {
		t.Fatalf("GetBook: unexpected error: %v", err)
	}

	if view.Stats.Total != 2 || view.Stats.Available != 2 {
		t.Fatalf("expected 2/2 copies, got %+v", view.Stats)
	}

	lent, err := svc.BorrowBookCopy(ctx, book.ID)
	if err != nil {
		t.Fatalf("BorrowBookCopy: unexpected error: %v", err)
	}

	// One of the registered copies of the book is issued.
	if lent.ID != first.ID && lent.ID != second.ID {
		t.Fatalf("borrowed copy %s does not belong to the book", lent.ID)
	}

	if lent.Status != domain.CopyStatusOnLoan {
		t.Fatalf("expected %q, got %q", domain.CopyStatusOnLoan, lent.Status)
	}

	view, err = svc.GetBook(ctx, book.ID)
	if err != nil {
		t.Fatalf("GetBook: unexpected error: %v", err)
	}

	if view.Stats.Available != 1 || view.Stats.OnLoan != 1 {
		t.Fatalf("expected 1 available / 1 on loan, got %+v", view.Stats)
	}

	// Borrowing an already taken copy again is not allowed — but the book still has one.
	if _, err := svc.BorrowBookCopy(ctx, book.ID); err != nil {
		t.Fatalf("BorrowBookCopy: unexpected error: %v", err)
	}

	if _, err := svc.BorrowBookCopy(ctx, book.ID); !errors.Is(err, domain.ErrNoAvailableCopies) {
		t.Fatalf("want %v, got %v", domain.ErrNoAvailableCopies, err)
	}

	returned, err := svc.ReturnBookCopy(ctx, lent.ID)
	if err != nil {
		t.Fatalf("ReturnBookCopy: unexpected error: %v", err)
	}

	if returned.Status != domain.CopyStatusAvailable {
		t.Fatalf("expected %q, got %q", domain.CopyStatusAvailable, returned.Status)
	}

	// Returning an already returned copy is a precondition error.
	if _, err := svc.ReturnBookCopy(ctx, lent.ID); !errors.Is(err, domain.ErrCopyNotAvailable) {
		t.Fatalf("want %v, got %v", domain.ErrCopyNotAvailable, err)
	}
}

func TestDeleteBookGuardsActiveLoans(t *testing.T) {

	ctx := context.Background()
	svc := newService(t)

	book := createBook(t, svc, isbnA, "Working in Pieces")

	if _, err := svc.AddBookCopy(ctx, book.ID, "BC-000010"); err != nil {
		t.Fatalf("AddBookCopy: unexpected error: %v", err)
	}

	lent, err := svc.BorrowBookCopy(ctx, book.ID)
	if err != nil {
		t.Fatalf("BorrowBookCopy: unexpected error: %v", err)
	}

	if err := svc.DeleteBook(ctx, book.ID); !errors.Is(err, domain.ErrBookHasActiveLoans) {
		t.Fatalf("want %v, got %v", domain.ErrBookHasActiveLoans, err)
	}

	if _, err := svc.ReturnBookCopy(ctx, lent.ID); err != nil {
		t.Fatalf("ReturnBookCopy: unexpected error: %v", err)
	}

	if err := svc.DeleteBook(ctx, book.ID); err != nil {
		t.Fatalf("DeleteBook: unexpected error: %v", err)
	}

	if _, err := svc.GetBook(ctx, book.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want %v, got %v", domain.ErrNotFound, err)
	}

	// The ISBN is released together with the book.
	if _, err := svc.CreateBook(ctx, service.CreateBookInput{
		ISBN: isbnA, Title: "Again", Author: "Author", PublishedYear: 2022,
	}); err != nil {
		t.Fatalf("CreateBook after delete: unexpected error: %v", err)
	}
}

func TestUpdateBookPartial(t *testing.T) {

	ctx := context.Background()
	svc := newService(t)

	book := createBook(t, svc, isbnA, "Original Title")

	year := int32(2001)

	view, err := svc.UpdateBook(ctx, book.ID, domain.BookUpdate{PublishedYear: &year})
	if err != nil {
		t.Fatalf("UpdateBook: unexpected error: %v", err)
	}

	if view.Book.PublishedYear != 2001 {
		t.Fatalf("year not updated: %d", view.Book.PublishedYear)
	}

	if view.Book.Title != "Original Title" {
		t.Fatalf("title must stay: %q", view.Book.Title)
	}

	// An empty update changes nothing.
	if _, err := svc.UpdateBook(ctx, book.ID, domain.BookUpdate{}); err != nil {
		t.Fatalf("UpdateBook: unexpected error: %v", err)
	}

	// Unknown book.
	if _, err := svc.UpdateBook(ctx, uuid.New(), domain.BookUpdate{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want %v, got %v", domain.ErrNotFound, err)
	}
}

func TestListBooksSearchAndPaging(t *testing.T) {

	ctx := context.Background()
	svc := newService(t)

	createBook(t, svc, isbnA, "The Go Programming Language")
	createBook(t, svc, isbnB, "Structure and Interpretation of Computer Programs")

	views, total, err := svc.ListBooks(ctx, domain.BookFilter{Query: "go"})
	if err != nil {
		t.Fatalf("ListBooks: unexpected error: %v", err)
	}

	if total != 1 || len(views) != 1 {
		t.Fatalf("expected single match, got total=%d len=%d", total, len(views))
	}

	// Search by the author that createBook sets.
	_, total, err = svc.ListBooks(ctx, domain.BookFilter{Query: "AUTHOR"})
	if err != nil {
		t.Fatalf("ListBooks: unexpected error: %v", err)
	}

	if total != 2 {
		t.Fatalf("case-insensitive author search expected 2, got %d", total)
	}

	// A page shorter than the total number of matches.
	views, total, err = svc.ListBooks(ctx, domain.BookFilter{Limit: 1})
	if err != nil {
		t.Fatalf("ListBooks: unexpected error: %v", err)
	}

	if total != 2 {
		t.Fatalf("total must count all matches, got %d", total)
	}

	if len(views) != 1 {
		t.Fatalf("limit ignored, got %d items", len(views))
	}

	// An offset past the end of the result set gives an empty page without an error.
	views, total, err = svc.ListBooks(ctx, domain.BookFilter{Offset: 10})
	if err != nil {
		t.Fatalf("ListBooks: unexpected error: %v", err)
	}

	if len(views) != 0 || total != 2 {
		t.Fatalf("expected empty page, got len=%d total=%d", len(views), total)
	}
}

func TestListBooksCapsLimit(t *testing.T) {

	ctx := context.Background()
	svc := newService(t)

	// More than MaxListLimit books, to check that the page is truncated.
	for i := range service.MaxListLimit + 5 {
		createBook(t, svc, isbn13(i), "Book")
	}

	views, total, err := svc.ListBooks(ctx, domain.BookFilter{Limit: service.MaxListLimit + 50})
	if err != nil {
		t.Fatalf("ListBooks: unexpected error: %v", err)
	}

	if total != service.MaxListLimit+5 {
		t.Fatalf("total must count all matches, got %d", total)
	}

	if len(views) != service.MaxListLimit {
		t.Fatalf("limit must be capped at %d, got %d", service.MaxListLimit, len(views))
	}

	// A negative limit means "default", not "no limit".
	views, _, err = svc.ListBooks(ctx, domain.BookFilter{Limit: -5})
	if err != nil {
		t.Fatalf("ListBooks: unexpected error: %v", err)
	}

	if len(views) != service.DefaultListLimit {
		t.Fatalf("expected default page %d, got %d", service.DefaultListLimit, len(views))
	}
}

// isbn13 builds a valid ISBN-13 from a counter: 12 digits plus a check digit.
func isbn13(seq int) string {
	base := fmt.Sprintf("978%09d", seq)

	sum := 0
	for i := range len(base) {
		digit := int(base[i] - '0')
		if i%2 == 1 {
			digit *= 3
		}

		sum += digit
	}

	return base + strconv.Itoa((10-sum%10)%10)
}

func TestBorrowUnknownBook(t *testing.T) {

	_, err := newService(t).BorrowBookCopy(context.Background(), uuid.New())
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want %v, got %v", domain.ErrNotFound, err)
	}
}

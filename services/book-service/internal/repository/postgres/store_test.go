package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/sapelyuk/smart-library/services/book-service/internal/domain"
	"github.com/sapelyuk/smart-library/services/book-service/internal/repository"
	"github.com/sapelyuk/smart-library/services/book-service/internal/repository/postgres"
	"github.com/sapelyuk/smart-library/services/book-service/internal/testdb"
)

var (
	_ repository.BookRepository = (*postgres.Store)(nil)
	_ repository.CopyRepository = (*postgres.Store)(nil)
)

// newStore returns a store over a freshly truncated PostgreSQL database.
func newStore(t *testing.T) *postgres.Store {
	t.Helper()

	return postgres.NewStore(testdb.New(t))
}

// newBook builds a valid book entity for the tests.
func newBook(t *testing.T, isbn, title string) *domain.Book {
	t.Helper()

	book, err := domain.NewBook(domain.NewBookParams{
		ISBN:          isbn,
		Title:         title,
		Author:        "Author",
		PublishedYear: 2020,
	})
	if err != nil {
		t.Fatalf("NewBook: unexpected error: %v", err)
	}

	return book
}

func TestBookCRUD(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	book := newBook(t, "978-0-13-419044-0", "Original")

	if err := store.Create(ctx, book); err != nil {
		t.Fatalf("Create: unexpected error: %v", err)
	}

	// The ISBN unique index is enforced by the database, not by a pre-check.
	if err := store.Create(ctx, newBook(t, "978-0-13-419044-0", "Other")); !errors.Is(err, domain.ErrISBNAlreadyExists) {
		t.Fatalf("want %v, got %v", domain.ErrISBNAlreadyExists, err)
	}

	fetched, err := store.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatalf("GetByID: unexpected error: %v", err)
	}

	if fetched.Title != "Original" {
		t.Fatalf("title = %q, want %q", fetched.Title, "Original")
	}

	byISBN, err := store.GetByISBN(ctx, book.ISBN)
	if err != nil {
		t.Fatalf("GetByISBN: unexpected error: %v", err)
	}

	if byISBN.ID != book.ID {
		t.Fatalf("ISBN lookup returned %s, want %s", byISBN.ID, book.ID)
	}

	title := "Updated"
	if err := fetched.Apply(domain.BookUpdate{Title: &title}); err != nil {
		t.Fatalf("Apply: unexpected error: %v", err)
	}

	if err := store.Update(ctx, fetched); err != nil {
		t.Fatalf("Update: unexpected error: %v", err)
	}

	updated, err := store.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatalf("GetByID: unexpected error: %v", err)
	}

	if updated.Title != "Updated" {
		t.Fatalf("update lost: %q", updated.Title)
	}

	// Updating an unknown book is reported as not found.
	if err := store.Update(ctx, newBook(t, "978-0-306-40615-7", "Ghost")); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want %v, got %v", domain.ErrNotFound, err)
	}

	if err := store.Delete(ctx, book.ID); err != nil {
		t.Fatalf("Delete: unexpected error: %v", err)
	}

	if _, err := store.GetByID(ctx, book.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want %v, got %v", domain.ErrNotFound, err)
	}

	if err := store.Delete(ctx, book.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second delete must report not found, got %v", err)
	}
}

func TestBookCyrillicSearch(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	create := func(isbn, title, author string) {
		t.Helper()

		book := newBook(t, isbn, title)
		book.Author = author

		if err := store.Create(ctx, book); err != nil {
			t.Fatalf("Create %q: unexpected error: %v", title, err)
		}
	}

	create("978-0-13-419044-0", "Мастер и Маргарита", "Михаил Булгаков")
	create("978-0-306-40615-7", "Преступление и наказание", "Фёдор Достоевский")

	// Case-insensitive Cyrillic search must match regardless of the case.
	books, total, err := store.List(ctx, domain.BookFilter{Query: "МАРГАРИТА", Limit: 20})
	if err != nil {
		t.Fatalf("List: unexpected error: %v", err)
	}

	if total != 1 || len(books) != 1 {
		t.Fatalf("expected a single match for the Cyrillic query, got total=%d len=%d", total, len(books))
	}

	if books[0].Title != "Мастер и Маргарита" {
		t.Fatalf("matched %q, want %q", books[0].Title, "Мастер и Маргарита")
	}

	// The author index must work the same way.
	_, total, err = store.List(ctx, domain.BookFilter{Query: "достоевский", Limit: 20})
	if err != nil {
		t.Fatalf("List: unexpected error: %v", err)
	}

	if total != 1 {
		t.Fatalf("expected a single match for the author, got %d", total)
	}
}

func TestCopyInventory(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	book := newBook(t, "978-0-13-419044-0", "Catalog")
	if err := store.Create(ctx, book); err != nil {
		t.Fatalf("Create: unexpected error: %v", err)
	}

	// A copy of a non-existent book.
	if err := store.CreateCopy(ctx, &domain.Copy{ID: uuid.New(), BookID: uuid.New(), Barcode: "X"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want %v, got %v", domain.ErrNotFound, err)
	}

	var ids []uuid.UUID

	for i := range 3 {
		item, err := domain.NewCopy(book.ID, fmt.Sprintf("BC-%03d", i))
		if err != nil {
			t.Fatalf("NewCopy: unexpected error: %v", err)
		}

		if err := store.CreateCopy(ctx, item); err != nil {
			t.Fatalf("CreateCopy: unexpected error: %v", err)
		}

		ids = append(ids, item.ID)
	}

	// A duplicate barcode is rejected by the unique index.
	if err := store.CreateCopy(ctx, &domain.Copy{ID: uuid.New(), BookID: book.ID, Barcode: "BC-000", Status: domain.CopyStatusAvailable}); !errors.Is(err, domain.ErrBarcodeAlreadyExists) {
		t.Fatalf("want %v, got %v", domain.ErrBarcodeAlreadyExists, err)
	}

	stats, err := store.Stats(ctx, book.ID)
	if err != nil {
		t.Fatalf("Stats: unexpected error: %v", err)
	}

	if stats.Total != 3 || stats.Available != 3 {
		t.Fatalf("unexpected stats: %+v", stats)
	}

	if err := store.UpdateStatus(ctx, ids[2], domain.CopyStatusLost); err != nil {
		t.Fatalf("UpdateStatus: unexpected error: %v", err)
	}

	if err := store.UpdateStatus(ctx, ids[2], domain.CopyStatus("BROKEN")); !errors.Is(err, domain.ErrInvalidCopyStatus) {
		t.Fatalf("want %v, got %v", domain.ErrInvalidCopyStatus, err)
	}

	stats, err = store.Stats(ctx, book.ID)
	if err != nil {
		t.Fatalf("Stats: unexpected error: %v", err)
	}

	// A lost copy counts into Total but not into Available.
	if stats.Total != 3 || stats.Available != 2 {
		t.Fatalf("lost copy must stay out of Available: %+v", stats)
	}

	copies, err := store.ListByBook(ctx, book.ID)
	if err != nil {
		t.Fatalf("ListByBook: unexpected error: %v", err)
	}

	if len(copies) != 3 {
		t.Fatalf("expected 3 copies, got %d", len(copies))
	}
}

func TestAcquireAvailable(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	book := newBook(t, "978-0-13-419044-0", "Queue")
	if err := store.Create(ctx, book); err != nil {
		t.Fatalf("Create: unexpected error: %v", err)
	}

	// The oldest available copy is handed out first.
	var first *domain.Copy

	for i := range 2 {
		item, err := domain.NewCopy(book.ID, fmt.Sprintf("BC-%03d", i))
		if err != nil {
			t.Fatalf("NewCopy: unexpected error: %v", err)
		}

		if err := store.CreateCopy(ctx, item); err != nil {
			t.Fatalf("CreateCopy: unexpected error: %v", err)
		}

		if i == 0 {
			first = item
		}
	}

	acquired, err := store.AcquireAvailable(ctx, book.ID)
	if err != nil {
		t.Fatalf("AcquireAvailable: unexpected error: %v", err)
	}

	if acquired.ID != first.ID {
		t.Fatalf("acquired %s, want the oldest copy %s", acquired.ID, first.ID)
	}

	if acquired.Status != domain.CopyStatusOnLoan {
		t.Fatalf("status = %q, want %q", acquired.Status, domain.CopyStatusOnLoan)
	}

	// Exhausting the queue is a precondition error.
	if _, err := store.AcquireAvailable(ctx, book.ID); err != nil {
		t.Fatalf("AcquireAvailable: unexpected error: %v", err)
	}

	if _, err := store.AcquireAvailable(ctx, book.ID); !errors.Is(err, domain.ErrNoAvailableCopies) {
		t.Fatalf("want %v, got %v", domain.ErrNoAvailableCopies, err)
	}

	// An unknown book is not found.
	if _, err := store.AcquireAvailable(ctx, uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want %v, got %v", domain.ErrNotFound, err)
	}
}

func TestAcquireAvailableIsAtomic(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	book := newBook(t, "978-0-13-419044-0", "Concurrency")
	if err := store.Create(ctx, book); err != nil {
		t.Fatalf("Create: unexpected error: %v", err)
	}

	const copies = 8

	for i := range copies {
		item, err := domain.NewCopy(book.ID, fmt.Sprintf("BC-%03d", i))
		if err != nil {
			t.Fatalf("NewCopy: unexpected error: %v", err)
		}

		if err := store.CreateCopy(ctx, item); err != nil {
			t.Fatalf("CreateCopy: unexpected error: %v", err)
		}
	}

	// Parallel issuing must not hand out the same copy twice. FOR UPDATE
	// SKIP LOCKED makes the losers skip the locked row instead of blocking.
	const workers = 16

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		taken = make(map[uuid.UUID]int)
	)

	for range workers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			item, err := store.AcquireAvailable(ctx, book.ID)

			mu.Lock()
			defer mu.Unlock()

			if err == nil {
				taken[item.ID]++
			}
		}()
	}

	wg.Wait()

	if len(taken) != copies {
		t.Fatalf("expected %d distinct copies handed out, got %d", copies, len(taken))
	}

	for id, count := range taken {
		if count != 1 {
			t.Fatalf("copy %s handed out %d times", id, count)
		}
	}

	stats, err := store.Stats(ctx, book.ID)
	if err != nil {
		t.Fatalf("Stats: unexpected error: %v", err)
	}

	if stats.Available != 0 || stats.OnLoan != copies {
		t.Fatalf("expected all copies on loan: %+v", stats)
	}
}

func TestDeleteBookRemovesCopies(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	book := newBook(t, "978-0-13-419044-0", "With copies")
	if err := store.Create(ctx, book); err != nil {
		t.Fatalf("Create: unexpected error: %v", err)
	}

	item, err := domain.NewCopy(book.ID, "BC-000")
	if err != nil {
		t.Fatalf("NewCopy: unexpected error: %v", err)
	}

	if err := store.CreateCopy(ctx, item); err != nil {
		t.Fatalf("CreateCopy: unexpected error: %v", err)
	}

	// The copies are removed together with the book, so ON DELETE RESTRICT does
	// not block the delete.
	if err := store.Delete(ctx, book.ID); err != nil {
		t.Fatalf("Delete: unexpected error: %v", err)
	}

	if _, err := store.GetCopyByID(ctx, item.ID); !errors.Is(err, domain.ErrCopyNotFound) {
		t.Fatalf("copies must be removed with the book, got %v", err)
	}
}

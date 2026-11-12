// Package service implements the use cases of the book service on top of the
// repository contracts. It owns business rules that do not belong to a single
// entity, such as ISBN uniqueness and copy availability.
package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/sapelyuk/smart-library/services/book-service/internal/domain"
	"github.com/sapelyuk/smart-library/services/book-service/internal/events"
	"github.com/sapelyuk/smart-library/services/book-service/internal/repository"
)

// Listing limits applied to the ListBooks request.
const (
	DefaultListLimit = 20
	MaxListLimit     = 100
)

// BookView is a catalog entry enriched with copy statistics.
type BookView struct {
	Book  *domain.Book
	Stats domain.CopyStats
}

// CreateBookInput is the raw input of the CreateBook use case.
type CreateBookInput struct {
	ISBN          string
	Title         string
	Author        string
	Publisher     string
	PublishedYear int32
}

// BookService orchestrates repositories and enforces cross-entity rules.
type BookService struct {
	books     repository.BookRepository
	copies    repository.CopyRepository
	publisher events.Publisher
	tenantID  uuid.UUID
	log       *slog.Logger
}

// NewBookService wires the service with its repositories.
func NewBookService(books repository.BookRepository, copies repository.CopyRepository) *BookService {
	return &BookService{books: books, copies: copies, log: slog.Default()}
}

// WithPublisher configures best-effort event publishing for this service.
func (s *BookService) WithPublisher(publisher events.Publisher, tenantID uuid.UUID, log *slog.Logger) *BookService {
	s.publisher = publisher
	s.tenantID = tenantID
	if log != nil {
		s.log = log
	}

	return s
}

// CreateBook registers a new edition in the catalog.
func (s *BookService) CreateBook(ctx context.Context, input CreateBookInput) (*domain.Book, error) {
	book, err := domain.NewBook(domain.NewBookParams{
		ISBN:          input.ISBN,
		Title:         input.Title,
		Author:        input.Author,
		Publisher:     input.Publisher,
		PublishedYear: input.PublishedYear,
	})
	if err != nil {
		return nil, err
	}

	if err := s.books.Create(ctx, book); err != nil {
		return nil, fmt.Errorf("create book: %w", err)
	}

	s.publish(ctx, events.TypeBookCreated, events.BookCreated{BookPayload: s.bookPayload(book)})

	return book, nil
}

// GetBook returns a book with its copy statistics.
func (s *BookService) GetBook(ctx context.Context, id uuid.UUID) (*BookView, error) {
	book, err := s.books.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	view, err := s.view(ctx, book)
	if err != nil {
		return nil, err
	}

	s.publish(ctx, events.TypeBookViewed, events.BookViewed{
		TenantID:   s.tenantID.String(),
		BookID:     book.ID.String(),
		OccurredAt: time.Now().UTC(),
	})

	return view, nil
}

// ListBooks returns a page of the catalog and the total match count.
func (s *BookService) ListBooks(ctx context.Context, filter domain.BookFilter) ([]BookView, int, error) {
	filter.Limit = normalizeLimit(filter.Limit)
	if filter.Offset < 0 {
		filter.Offset = 0
	}

	books, total, err := s.books.List(ctx, filter)
	if err != nil {
		return nil, 0, fmt.Errorf("list books: %w", err)
	}

	views := make([]BookView, 0, len(books))

	for _, book := range books {
		view, err := s.view(ctx, book)
		if err != nil {
			return nil, 0, err
		}

		views = append(views, *view)
	}

	return views, total, nil
}

// UpdateBook applies a partial update and returns the refreshed book.
func (s *BookService) UpdateBook(ctx context.Context, id uuid.UUID, update domain.BookUpdate) (*BookView, error) {
	book, err := s.books.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if err := book.Apply(update); err != nil {
		return nil, err
	}

	if err := s.books.Update(ctx, book); err != nil {
		return nil, fmt.Errorf("update book: %w", err)
	}

	s.publish(ctx, events.TypeBookUpdated, events.BookUpdated{BookPayload: s.bookPayload(book)})

	return s.view(ctx, book)
}

// DeleteBook removes a book as long as no copy is currently on loan.
func (s *BookService) DeleteBook(ctx context.Context, id uuid.UUID) error {
	stats, err := s.copies.Stats(ctx, id)
	if err != nil {
		return err
	}

	if stats.OnLoan > 0 {
		return fmt.Errorf("%w: %d copies on loan", domain.ErrBookHasActiveLoans, stats.OnLoan)
	}

	if err := s.books.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete book: %w", err)
	}

	s.publish(ctx, events.TypeBookDeleted, events.BookDeleted{
		TenantID: s.tenantID.String(),
		BookID:   id.String(),
	})

	return nil
}

// AddBookCopy puts a new physical copy into the inventory.
func (s *BookService) AddBookCopy(ctx context.Context, bookID uuid.UUID, barcode string) (*domain.Copy, error) {
	if _, err := s.books.GetByID(ctx, bookID); err != nil {
		return nil, err
	}

	copy, err := domain.NewCopy(bookID, barcode)
	if err != nil {
		return nil, err
	}

	if err := s.copies.CreateCopy(ctx, copy); err != nil {
		return nil, fmt.Errorf("add copy: %w", err)
	}

	return copy, nil
}

// ListBookCopies returns the inventory of the book.
func (s *BookService) ListBookCopies(ctx context.Context, bookID uuid.UUID) ([]*domain.Copy, error) {
	copies, err := s.copies.ListByBook(ctx, bookID)
	if err != nil {
		return nil, fmt.Errorf("list copies: %w", err)
	}

	return copies, nil
}

// BorrowBookCopy marks the first available copy as ON_LOAN. Called by the
// future Loan Service over gRPC.
func (s *BookService) BorrowBookCopy(ctx context.Context, bookID uuid.UUID) (*domain.Copy, error) {
	copy, err := s.copies.AcquireAvailable(ctx, bookID)
	if err != nil {
		return nil, fmt.Errorf("borrow copy: %w", err)
	}

	return copy, nil
}

// ReturnBookCopy puts a borrowed copy back into the catalog.
func (s *BookService) ReturnBookCopy(ctx context.Context, copyID uuid.UUID) (*domain.Copy, error) {
	copy, err := s.copies.GetCopyByID(ctx, copyID)
	if err != nil {
		return nil, err
	}

	if err := copy.Return(); err != nil {
		return nil, err
	}

	if err := s.copies.UpdateStatus(ctx, copy.ID, domain.CopyStatusAvailable); err != nil {
		return nil, fmt.Errorf("return copy: %w", err)
	}

	return copy, nil
}

func (s *BookService) view(ctx context.Context, book *domain.Book) (*BookView, error) {
	stats, err := s.copies.Stats(ctx, book.ID)
	if err != nil {
		return nil, fmt.Errorf("copy stats: %w", err)
	}

	return &BookView{Book: book, Stats: stats}, nil
}

func (s *BookService) bookPayload(book *domain.Book) events.BookPayload {
	return events.BookPayload{
		TenantID:      s.tenantID.String(),
		BookID:        book.ID.String(),
		ISBN:          string(book.ISBN),
		Title:         book.Title,
		Author:        book.Author,
		Publisher:     book.Publisher,
		PublishedYear: book.PublishedYear,
	}
}

func (s *BookService) publish(ctx context.Context, eventType string, payload any) {
	if s.publisher == nil {
		return
	}

	if err := s.publisher.Publish(ctx, eventType, payload); err != nil {
		s.log.WarnContext(ctx, "cannot publish a domain event",
			"event_type", eventType, "error", err)
	}
}

func normalizeLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultListLimit
	case limit > MaxListLimit:
		return MaxListLimit
	default:
		return int(limit)
	}
}

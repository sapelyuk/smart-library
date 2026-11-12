package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	wireevents "github.com/sapelyuk/smart-library/pkg/events"
	"github.com/sapelyuk/smart-library/services/book-service/internal/domain"
	"github.com/sapelyuk/smart-library/services/book-service/internal/repository"
	"github.com/sapelyuk/smart-library/services/book-service/internal/service"
)

type capturedEvent struct {
	routingKey string
	body       []byte
	headers    wireevents.Headers
}

type recordingTransport struct {
	events []capturedEvent
	err    error
}

func (p *recordingTransport) Publish(_ context.Context, routingKey string, body []byte, headers wireevents.Headers) error {
	p.events = append(p.events, capturedEvent{
		routingKey: routingKey,
		body:       append([]byte(nil), body...),
		headers:    headers,
	})

	return p.err
}

type eventBookStore struct {
	books map[uuid.UUID]*domain.Book
}

func newEventBookStore() *eventBookStore {
	return &eventBookStore{books: make(map[uuid.UUID]*domain.Book)}
}

func (r *eventBookStore) Create(_ context.Context, book *domain.Book) error {
	r.books[book.ID] = book
	return nil
}

func (r *eventBookStore) Update(_ context.Context, book *domain.Book) error {
	r.books[book.ID] = book
	return nil
}

func (r *eventBookStore) Delete(_ context.Context, id uuid.UUID) error {
	if _, ok := r.books[id]; !ok {
		return domain.ErrNotFound
	}
	delete(r.books, id)
	return nil
}

func (r *eventBookStore) GetByID(_ context.Context, id uuid.UUID) (*domain.Book, error) {
	book, ok := r.books[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return book, nil
}

func (r *eventBookStore) GetByISBN(_ context.Context, isbn domain.ISBN) (*domain.Book, error) {
	for _, book := range r.books {
		if book.ISBN == isbn {
			return book, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (r *eventBookStore) List(context.Context, domain.BookFilter) ([]*domain.Book, int, error) {
	return nil, 0, nil
}

type eventCopyStore struct{}

func (eventCopyStore) CreateCopy(context.Context, *domain.Copy) error { return nil }
func (eventCopyStore) GetCopyByID(context.Context, uuid.UUID) (*domain.Copy, error) {
	return nil, domain.ErrNotFound
}
func (eventCopyStore) ListByBook(context.Context, uuid.UUID) ([]*domain.Copy, error) {
	return nil, nil
}
func (eventCopyStore) UpdateStatus(context.Context, uuid.UUID, domain.CopyStatus) error {
	return nil
}
func (eventCopyStore) Stats(context.Context, uuid.UUID) (domain.CopyStats, error) {
	return domain.CopyStats{}, nil
}
func (eventCopyStore) AcquireAvailable(context.Context, uuid.UUID) (*domain.Copy, error) {
	return nil, domain.ErrNoAvailableCopies
}

var _ repository.BookRepository = (*eventBookStore)(nil)
var _ repository.CopyRepository = eventCopyStore{}

func TestBookServicePublishesCatalogEvents(t *testing.T) {
	ctx := context.Background()
	transport := &recordingTransport{}
	publisher, err := wireevents.NewEventPublisher(transport)
	if err != nil {
		t.Fatalf("NewEventPublisher: %v", err)
	}

	const tenantID = "9ee8f5da-7d51-49fb-9836-411198e48ef1"
	parsedTenantID := uuid.MustParse(tenantID)
	books := newEventBookStore()
	svc := service.NewBookService(books, eventCopyStore{}).
		WithPublisher(publisher, parsedTenantID, nil)

	book, err := svc.CreateBook(ctx, service.CreateBookInput{
		ISBN:          isbnA,
		Title:         "The Go Programming Language",
		Author:        "Alan A. A. Donovan",
		Publisher:     "Addison-Wesley",
		PublishedYear: 2015,
	})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	created := assertEvent(t, transport.events, 0, "book.created", tenantID, book.ID.String())
	var createdPayload struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(created.Payload, &createdPayload); err != nil {
		t.Fatalf("decode book.created payload: %v", err)
	}
	if createdPayload.Title != "The Go Programming Language" {
		t.Fatalf("book.created title = %q", createdPayload.Title)
	}

	title := "The Go Programming Language, Second Edition"
	if _, err := svc.UpdateBook(ctx, book.ID, domain.BookUpdate{Title: &title}); err != nil {
		t.Fatalf("UpdateBook: %v", err)
	}
	updated := assertEvent(t, transport.events, 1, "book.updated", tenantID, book.ID.String())
	var updatedPayload struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(updated.Payload, &updatedPayload); err != nil {
		t.Fatalf("decode book.updated payload: %v", err)
	}
	if updatedPayload.Title != title {
		t.Fatalf("book.updated title = %q, want %q", updatedPayload.Title, title)
	}

	if _, err := svc.GetBook(ctx, book.ID); err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	viewed := assertEvent(t, transport.events, 2, "book.viewed", tenantID, book.ID.String())
	var viewedPayload struct {
		OccurredAt string `json:"occurred_at"`
	}
	if err := json.Unmarshal(viewed.Payload, &viewedPayload); err != nil {
		t.Fatalf("decode book.viewed payload: %v", err)
	}
	if _, err := time.Parse(time.RFC3339Nano, viewedPayload.OccurredAt); err != nil {
		t.Fatalf("book.viewed occurred_at = %q, want RFC3339 timestamp: %v", viewedPayload.OccurredAt, err)
	}

	if err := svc.DeleteBook(ctx, book.ID); err != nil {
		t.Fatalf("DeleteBook: %v", err)
	}
	assertEvent(t, transport.events, 3, "book.deleted", tenantID, book.ID.String())

	if _, err := svc.GetBook(ctx, book.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetBook deleted book: want %v, got %v", domain.ErrNotFound, err)
	}
	if len(transport.events) != 4 {
		t.Fatalf("event count after failed GetBook = %d, want 4", len(transport.events))
	}
}

func TestBookServiceContinuesWhenEventPublishingFails(t *testing.T) {
	transport := &recordingTransport{err: errors.New("broker unavailable")}
	publisher, err := wireevents.NewEventPublisher(transport)
	if err != nil {
		t.Fatalf("NewEventPublisher: %v", err)
	}

	var logOutput strings.Builder
	log := slog.New(slog.NewTextHandler(&logOutput, &slog.HandlerOptions{Level: slog.LevelWarn}))
	svc := service.NewBookService(newEventBookStore(), eventCopyStore{}).
		WithPublisher(publisher, uuid.MustParse("9ee8f5da-7d51-49fb-9836-411198e48ef1"), log)

	book, err := svc.CreateBook(context.Background(), service.CreateBookInput{
		ISBN: isbnA, Title: "The Go Programming Language", Author: "Alan A. A. Donovan",
		Publisher: "Addison-Wesley", PublishedYear: 2015,
	})
	if err != nil {
		t.Fatalf("CreateBook failed after publish error: %v", err)
	}

	if _, err := svc.GetBook(context.Background(), book.ID); err != nil {
		t.Fatalf("GetBook failed after publish error: %v", err)
	}
	if got := strings.Count(logOutput.String(), "cannot publish a domain event"); got != 2 {
		t.Fatalf("logged event publish warnings = %d, want 2; logs: %s", got, logOutput.String())
	}
	if !strings.Contains(logOutput.String(), "level=WARN") {
		t.Fatalf("publish failures were not logged at warning level: %s", logOutput.String())
	}
}

func TestBookServiceWorksWithoutPublisher(t *testing.T) {
	svc := service.NewBookService(newEventBookStore(), eventCopyStore{}).
		WithPublisher(nil, uuid.MustParse("9ee8f5da-7d51-49fb-9836-411198e48ef1"), nil)
	book, err := svc.CreateBook(context.Background(), service.CreateBookInput{
		ISBN: isbnA, Title: "The Go Programming Language", Author: "Alan A. A. Donovan",
		Publisher: "Addison-Wesley", PublishedYear: 2015,
	})
	if err != nil {
		t.Fatalf("CreateBook without publisher: %v", err)
	}
	if _, err := svc.GetBook(context.Background(), book.ID); err != nil {
		t.Fatalf("GetBook without publisher: %v", err)
	}
}

func assertEvent(t *testing.T, events []capturedEvent, index int, eventType, tenantID, bookID string) *wireevents.Event {
	t.Helper()
	if index >= len(events) {
		t.Fatalf("missing %s event at index %d (got %d)", eventType, index, len(events))
	}

	message := events[index]
	if message.routingKey != eventType {
		t.Fatalf("routing key = %q, want %q", message.routingKey, eventType)
	}

	event, err := wireevents.DecodeEvent(message.body)
	if err != nil {
		t.Fatalf("DecodeEvent(%s): %v", eventType, err)
	}
	if event.Type != eventType {
		t.Fatalf("envelope event type = %q, want %q", event.Type, eventType)
	}

	var payload struct {
		TenantID string `json:"tenant_id"`
		BookID   string `json:"book_id"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("decode %s payload: %v", eventType, err)
	}
	if payload.TenantID != tenantID || payload.BookID != bookID {
		t.Fatalf("%s payload = %+v, want tenant_id=%s book_id=%s", eventType, payload, tenantID, bookID)
	}
	if _, ok := message.headers[wireevents.HeaderEventID]; !ok {
		t.Fatalf("%s event missing envelope headers", eventType)
	}

	return event
}

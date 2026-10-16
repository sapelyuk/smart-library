package service_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/sapelyuk/smart-library/services/ai-service/internal/domain"
	"github.com/sapelyuk/smart-library/services/ai-service/internal/service"
)

// stubRecommender records Recommend calls and returns a canned Answer or error.
type stubRecommender struct {
	calls  []domain.Query
	answer domain.Answer
	err    error
}

func (s *stubRecommender) Recommend(_ context.Context, q domain.Query) (domain.Answer, error) {
	s.calls = append(s.calls, q)
	if s.err != nil {
		return domain.Answer{}, s.err
	}
	return s.answer, nil
}

// stubIndexer records Ingest calls and returns an optional error.
type stubIndexer struct {
	calls []domain.CatalogBook
	err   error
}

func (s *stubIndexer) Ingest(_ context.Context, b domain.CatalogBook) error {
	s.calls = append(s.calls, b)
	return s.err
}

// stubStore records DeleteBook/Stats calls and returns canned results.
type stubStore struct {
	deleteCalls []string
	deleteResp  domain.IndexDeletion
	deleteErr   error

	statsCalls int
	statsResp  domain.IndexStats
	statsErr   error
}

func (s *stubStore) DeleteBook(_ context.Context, bookID string) (domain.IndexDeletion, error) {
	s.deleteCalls = append(s.deleteCalls, bookID)
	if s.deleteErr != nil {
		return domain.IndexDeletion{}, s.deleteErr
	}
	return s.deleteResp, nil
}

func (s *stubStore) Stats(_ context.Context) (domain.IndexStats, error) {
	s.statsCalls++
	if s.statsErr != nil {
		return domain.IndexStats{}, s.statsErr
	}
	return s.statsResp, nil
}

// stubCatalog records Book calls and returns a canned CatalogBook or error.
type stubCatalog struct {
	calls []string
	book  domain.CatalogBook
	err   error
}

func (s *stubCatalog) Book(_ context.Context, bookID string) (domain.CatalogBook, error) {
	s.calls = append(s.calls, bookID)
	if s.err != nil {
		return domain.CatalogBook{}, s.err
	}
	return s.book, nil
}

var (
	_ service.Recommender = (*stubRecommender)(nil)
	_ service.Indexer     = (*stubIndexer)(nil)
	_ service.IndexStore  = (*stubStore)(nil)
	_ service.Catalog     = (*stubCatalog)(nil)
)

func newTestService(t *testing.T, rec *stubRecommender, idx *stubIndexer, st *stubStore, cat *stubCatalog) *service.Service {
	t.Helper()
	svc, err := service.New(rec, idx, st, cat, service.Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	return svc
}

var readerP = domain.Principal{
	UserID:    uuid.MustParse("11111111-1111-1111-1111-111111111111"),
	SessionID: uuid.MustParse("22222222-2222-2222-2222-222222222222"),
	Email:     "reader@example.com",
	Role:      domain.RoleReader,
}

var librarianP = domain.Principal{
	UserID:    uuid.MustParse("33333333-3333-3333-3333-333333333333"),
	SessionID: uuid.MustParse("44444444-4444-4444-4444-444444444444"),
	Email:     "librarian@example.com",
	Role:      domain.RoleLibrarian,
}
// --- New ---

func TestNewRejectsNilDeps(t *testing.T) {
	for _, tt := range []struct {
		name string
		fn   func() error
	}{
		{"recommender", func() error {
			_, err := service.New(nil, &stubIndexer{}, &stubStore{}, &stubCatalog{}, service.Config{})
			return err
		}},
		{"indexer", func() error {
			_, err := service.New(&stubRecommender{}, nil, &stubStore{}, &stubCatalog{}, service.Config{})
			return err
		}},
		{"store", func() error {
			_, err := service.New(&stubRecommender{}, &stubIndexer{}, nil, &stubCatalog{}, service.Config{})
			return err
		}},
		{"catalog", func() error {
			_, err := service.New(&stubRecommender{}, &stubIndexer{}, &stubStore{}, nil, service.Config{})
			return err
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.fn(); err == nil {
				t.Errorf("expected error for nil %s", tt.name)
			}
		})
	}
}

// --- Recommend ---

func TestRecommendHappyPath(t *testing.T) {
	rec := &stubRecommender{
		answer: domain.Answer{
			Text:      "I recommend these books for DDD.",
			SessionID: "sess-abc",
			Books: []domain.RecommendedBook{
				{BookID: "book-1", Title: "DDD Book", Author: "Evans"},
			},
			Source: domain.SourceGenerated,
		},
	}
	svc := newTestService(t, rec, &stubIndexer{}, &stubStore{}, &stubCatalog{})

	answer, err := svc.Recommend(context.Background(), readerP, service.RecommendInput{
		Query:     "  Where is the book on DDD?  ",
		SessionID: "sess-abc",
		Limit:     3,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if answer.Text != "I recommend these books for DDD." {
		t.Errorf("Text = %q, want %q", answer.Text, "I recommend these books for DDD.")
	}
	if answer.SessionID != "sess-abc" {
		t.Errorf("SessionID = %q, want %q", answer.SessionID, "sess-abc")
	}

	if len(rec.calls) != 1 {
		t.Fatalf("recommender called %d times, want 1", len(rec.calls))
	}
	q := rec.calls[0]
	if q.Text != "Where is the book on DDD?" {
		t.Errorf("query.Text = %q, want trimmed text", q.Text)
	}
	if q.SessionID != "sess-abc" {
		t.Errorf("query.SessionID = %q, want %q", q.SessionID, "sess-abc")
	}
	if q.Limit != 3 {
		t.Errorf("query.Limit = %d, want 3", q.Limit)
	}
}

func TestRecommendLimitNormalization(t *testing.T) {
	rec := &stubRecommender{answer: domain.Answer{Text: "answer", Source: domain.SourceGenerated}}
	svc := newTestService(t, rec, &stubIndexer{}, &stubStore{}, &stubCatalog{})

	for _, tt := range []struct {
		input int
		want  int
	}{
		{0, domain.DefaultLimit},
		{-5, domain.DefaultLimit},
		{domain.MaxLimit + 5, domain.MaxLimit},
		{domain.MaxLimit, domain.MaxLimit},
		{7, 7},
	} {
		t.Run("", func(t *testing.T) {
			rec.calls = nil
			rec.err = nil
			_, err := svc.Recommend(context.Background(), readerP, service.RecommendInput{
				Query: "question", Limit: tt.input,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(rec.calls) != 1 {
				t.Fatalf("recommender called %d times", len(rec.calls))
			}
			if rec.calls[0].Limit != tt.want {
				t.Errorf("limit = %d, want %d", rec.calls[0].Limit, tt.want)
			}
		})
	}
}

func TestRecommendSessionIDEcho(t *testing.T) {
	rec := &stubRecommender{
		answer: domain.Answer{
			Text:      "An answer without session.",
			SessionID: "",
			Source:    domain.SourceGenerated,
		},
	}
	svc := newTestService(t, rec, &stubIndexer{}, &stubStore{}, &stubCatalog{})

	answer, err := svc.Recommend(context.Background(), readerP, service.RecommendInput{
		Query:     "follow-up",
		SessionID: "my-session",
		Limit:     0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if answer.SessionID != "my-session" {
		t.Errorf("SessionID = %q, want caller's original", answer.SessionID)
	}
}

func TestRecommendValidationErrors(t *testing.T) {
	rec := &stubRecommender{answer: domain.Answer{Text: "ok", Source: domain.SourceGenerated}}
	svc := newTestService(t, rec, &stubIndexer{}, &stubStore{}, &stubCatalog{})

	for _, tt := range []struct {
		name  string
		input service.RecommendInput
		want  error
	}{
		{
			"empty query",
			service.RecommendInput{Query: "   ", SessionID: "s", Limit: 1},
			domain.ErrEmptyQuery,
		},
		{
			"too long query",
			service.RecommendInput{Query: string(make([]byte, domain.MaxQueryRunes+100)), SessionID: "s", Limit: 1},
			domain.ErrQueryTooLong,
		},
		{
			"invalid session id",
			service.RecommendInput{Query: "q", SessionID: "has space", Limit: 1},
			domain.ErrInvalidSessionID,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec.calls = nil
			_, err := svc.Recommend(context.Background(), readerP, tt.input)
			if !errors.Is(err, tt.want) {
				t.Errorf("error = %v, want %v", err, tt.want)
			}
			if len(rec.calls) != 0 {
				t.Error("recommender should not be called on validation failure")
			}
		})
	}
}

func TestRecommendErrorPropagation(t *testing.T) {
	wantErr := errors.New("backend is down")
	rec := &stubRecommender{err: wantErr}
	svc := newTestService(t, rec, &stubIndexer{}, &stubStore{}, &stubCatalog{})

	_, err := svc.Recommend(context.Background(), readerP, service.RecommendInput{Query: "q", Limit: 1})
	if !errors.Is(err, wantErr) {
		t.Errorf("wrapped error = %v, want %v", err, wantErr)
	}
}
// --- IngestBook ---

func TestIngestBookRequiresLibrarian(t *testing.T) {
	cat := &stubCatalog{}
	svc := newTestService(t, &stubRecommender{}, &stubIndexer{}, &stubStore{}, cat)

	_, err := svc.IngestBook(context.Background(), readerP, uuid.New().String())
	if !errors.Is(err, domain.ErrPermissionDenied) {
		t.Errorf("error = %v, want ErrPermissionDenied", err)
	}
	if len(cat.calls) != 0 {
		t.Error("catalog should not be called for unauthorized caller")
	}
}

func TestIngestBookHappyPath(t *testing.T) {
	bookID := uuid.New().String()
	catBook := domain.CatalogBook{
		ID:     bookID,
		Title:  "Clean Architecture",
		Author: "Robert C. Martin",
	}
	cat := &stubCatalog{book: catBook}
	idx := &stubIndexer{}
	svc := newTestService(t, &stubRecommender{}, idx, &stubStore{}, cat)

	book, err := svc.IngestBook(context.Background(), librarianP, bookID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if book.Title != "Clean Architecture" {
		t.Errorf("returned title = %q, want %q", book.Title, "Clean Architecture")
	}
	if len(cat.calls) != 1 || cat.calls[0] != bookID {
		t.Errorf("catalog calls = %v, want [%s]", cat.calls, bookID)
	}
	if len(idx.calls) != 1 {
		t.Fatalf("indexer called %d times, want 1", len(idx.calls))
	}
	if idx.calls[0].ID != bookID {
		t.Errorf("ingested book ID = %q, want %q", idx.calls[0].ID, bookID)
	}
}

func TestIngestBookInvalidID(t *testing.T) {
	cat := &stubCatalog{}
	svc := newTestService(t, &stubRecommender{}, &stubIndexer{}, &stubStore{}, cat)

	_, err := svc.IngestBook(context.Background(), librarianP, "not-a-uuid")
	if !errors.Is(err, domain.ErrInvalidBookID) {
		t.Errorf("error = %v, want ErrInvalidBookID", err)
	}
	if len(cat.calls) != 0 {
		t.Error("catalog should not be called for invalid ID")
	}
}

func TestIngestBookNotFound(t *testing.T) {
	cat := &stubCatalog{err: domain.ErrBookNotFound}
	svc := newTestService(t, &stubRecommender{}, &stubIndexer{}, &stubStore{}, cat)

	_, err := svc.IngestBook(context.Background(), librarianP, uuid.New().String())
	if !errors.Is(err, domain.ErrBookNotFound) {
		t.Errorf("error = %v, want ErrBookNotFound", err)
	}
}

func TestIngestBookInvalidRecord(t *testing.T) {
	cat := &stubCatalog{book: domain.CatalogBook{ID: uuid.New().String(), Title: ""}}
	idx := &stubIndexer{}
	svc := newTestService(t, &stubRecommender{}, idx, &stubStore{}, cat)

	_, err := svc.IngestBook(context.Background(), librarianP, cat.book.ID)
	if !errors.Is(err, domain.ErrInvalidBookID) {
		t.Errorf("error = %v, want ErrInvalidBookID", err)
	}
	if len(idx.calls) != 0 {
		t.Error("indexer should not be called for invalid record")
	}
}

func TestIngestBookIndexerError(t *testing.T) {
	wantErr := errors.New("index is down")
	idx := &stubIndexer{err: wantErr}
	cat := &stubCatalog{book: domain.CatalogBook{ID: uuid.New().String(), Title: "X"}}
	svc := newTestService(t, &stubRecommender{}, idx, &stubStore{}, cat)

	_, err := svc.IngestBook(context.Background(), librarianP, cat.book.ID)
	if !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want %v", err, wantErr)
	}
}

// --- SyncBook ---

func TestSyncBook(t *testing.T) {
	bookID := uuid.New().String()
	cat := &stubCatalog{book: domain.CatalogBook{ID: bookID, Title: "Synced"}}
	idx := &stubIndexer{}
	svc := newTestService(t, &stubRecommender{}, idx, &stubStore{}, cat)

	book, err := svc.SyncBook(context.Background(), bookID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if book.Title != "Synced" {
		t.Errorf("title = %q, want %q", book.Title, "Synced")
	}
	if len(idx.calls) != 1 {
		t.Fatalf("indexer called %d times, want 1", len(idx.calls))
	}
}

// --- DeleteBook ---

func TestDeleteBookRequiresLibrarian(t *testing.T) {
	store := &stubStore{}
	svc := newTestService(t, &stubRecommender{}, &stubIndexer{}, store, &stubCatalog{})

	_, err := svc.DeleteBook(context.Background(), readerP, uuid.New().String())
	if !errors.Is(err, domain.ErrPermissionDenied) {
		t.Errorf("error = %v, want ErrPermissionDenied", err)
	}
	if len(store.deleteCalls) != 0 {
		t.Error("store should not be called for unauthorized caller")
	}
}

func TestDeleteBookHappyPath(t *testing.T) {
	bookID := uuid.New().String()
	store := &stubStore{
		deleteResp: domain.IndexDeletion{Chunks: 3, Deleted: true},
	}
	svc := newTestService(t, &stubRecommender{}, &stubIndexer{}, store, &stubCatalog{})

	del, err := svc.DeleteBook(context.Background(), librarianP, bookID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if del.BookID != bookID {
		t.Errorf("BookID = %q, want %q", del.BookID, bookID)
	}
	if !del.Deleted {
		t.Error("Deleted should be true")
	}
	if del.Chunks != 3 {
		t.Errorf("Chunks = %d, want 3", del.Chunks)
	}
}

func TestDeleteBookStoreError(t *testing.T) {
	wantErr := errors.New("store down")
	store := &stubStore{deleteErr: wantErr}
	svc := newTestService(t, &stubRecommender{}, &stubIndexer{}, store, &stubCatalog{})

	_, err := svc.DeleteBook(context.Background(), librarianP, uuid.New().String())
	if !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want %v", err, wantErr)
	}
}

func TestDeleteBookInvalidID(t *testing.T) {
	store := &stubStore{}
	svc := newTestService(t, &stubRecommender{}, &stubIndexer{}, store, &stubCatalog{})

	_, err := svc.DeleteBook(context.Background(), librarianP, "not-uuid")
	if !errors.Is(err, domain.ErrInvalidBookID) {
		t.Errorf("error = %v, want ErrInvalidBookID", err)
	}
}

// --- RemoveBook ---

func TestRemoveBook(t *testing.T) {
	bookID := uuid.New().String()
	store := &stubStore{deleteResp: domain.IndexDeletion{Deleted: false}}
	svc := newTestService(t, &stubRecommender{}, &stubIndexer{}, store, &stubCatalog{})

	del, err := svc.RemoveBook(context.Background(), bookID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if del.BookID != bookID {
		t.Errorf("BookID = %q, want %q", del.BookID, bookID)
	}
	if del.Deleted {
		t.Error("Deleted should be false")
	}
}

// --- IndexStatus ---

func TestIndexStatusRequiresLibrarian(t *testing.T) {
	store := &stubStore{}
	svc := newTestService(t, &stubRecommender{}, &stubIndexer{}, store, &stubCatalog{})

	_, err := svc.IndexStatus(context.Background(), readerP)
	if !errors.Is(err, domain.ErrPermissionDenied) {
		t.Errorf("error = %v, want ErrPermissionDenied", err)
	}
	if store.statsCalls != 0 {
		t.Error("store.Stats should not be called for unauthorized caller")
	}
}

func TestIndexStatusHappyPath(t *testing.T) {
	store := &stubStore{statsResp: domain.IndexStats{Books: 42, Chunks: 128}}
	svc := newTestService(t, &stubRecommender{}, &stubIndexer{}, store, &stubCatalog{})

	stats, err := svc.IndexStatus(context.Background(), librarianP)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stats.Books != 42 {
		t.Errorf("Books = %d, want 42", stats.Books)
	}
	if stats.Chunks != 128 {
		t.Errorf("Chunks = %d, want 128", stats.Chunks)
	}
}

func TestIndexStatusError(t *testing.T) {
	wantErr := errors.New("store down")
	store := &stubStore{statsErr: wantErr}
	svc := newTestService(t, &stubRecommender{}, &stubIndexer{}, store, &stubCatalog{})

	_, err := svc.IndexStatus(context.Background(), librarianP)
	if !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want %v", err, wantErr)
	}
}
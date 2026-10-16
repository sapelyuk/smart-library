package handler_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	aiv1 "github.com/sapelyuk/smart-library/services/ai-service/gen/go/ai/v1"
	"github.com/sapelyuk/smart-library/services/ai-service/internal/auth"
	"github.com/sapelyuk/smart-library/services/ai-service/internal/domain"
	"github.com/sapelyuk/smart-library/services/ai-service/internal/handler"
	"github.com/sapelyuk/smart-library/services/ai-service/internal/service"
)

// --- local stubs (same as service_test, isolated to handler_test) ---

type stubRec struct {
	answer domain.Answer
	err    error
}

func (s *stubRec) Recommend(_ context.Context, q domain.Query) (domain.Answer, error) {
	if s.err != nil {
		return domain.Answer{}, s.err
	}
	return s.answer, nil
}

type stubIdx struct{ err error }

func (s *stubIdx) Ingest(_ context.Context, b domain.CatalogBook) error { return s.err }

type stubStore struct {
	delResp domain.IndexDeletion
	delErr  error
	stats   domain.IndexStats
	statsErr error
}

func (s *stubStore) DeleteBook(_ context.Context, id string) (domain.IndexDeletion, error) {
	if s.delErr != nil {
		return domain.IndexDeletion{}, s.delErr
	}
	return s.delResp, nil
}

func (s *stubStore) Stats(_ context.Context) (domain.IndexStats, error) {
	if s.statsErr != nil {
		return domain.IndexStats{}, s.statsErr
	}
	return s.stats, nil
}

type stubCat struct {
	book domain.CatalogBook
	err  error
}

func (s *stubCat) Book(_ context.Context, id string) (domain.CatalogBook, error) {
	if s.err != nil {
		return domain.CatalogBook{}, s.err
	}
	return s.book, nil
}

func newSvc(t *testing.T, rec *stubRec, idx *stubIdx, st *stubStore, cat *stubCat) *service.Service {
	t.Helper()
	svc, err := service.New(rec, idx, st, cat, service.Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc
}

func withPrincipal(ctx context.Context) context.Context {
	return auth.WithPrincipal(ctx, domain.Principal{
		UserID:    uuid.MustParse("33333333-3333-3333-3333-333333333333"),
		SessionID: uuid.MustParse("44444444-4444-4444-4444-444444444444"),
		Email:     "librarian@example.com",
		Role:      domain.RoleLibrarian,
	})
}

func withReader(ctx context.Context) context.Context {
	return auth.WithPrincipal(ctx, domain.Principal{
		UserID:    uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		SessionID: uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		Email:     "reader@example.com",
		Role:      domain.RoleReader,
	})
}

var testBookID = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
// --- Recommend ---

func TestRecommendSuccess(t *testing.T) {
	rec := &stubRec{answer: domain.Answer{
		Text:      "I recommend these.",
		SessionID: "sess-1",
		Books: []domain.RecommendedBook{
			{BookID: "b1", Title: "Book One", Author: "A"},
		},
		Source: domain.SourceGenerated,
	}}
	svc := newSvc(t, rec, &stubIdx{}, &stubStore{}, &stubCat{})
	srv := handler.NewGRPCServer(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx := withReader(context.Background())
	resp, err := srv.Recommend(ctx, &aiv1.RecommendRequest{
		Query:     "books about DDD",
		SessionId: "sess-1",
		Limit:     3,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	recResp := resp.GetRecommendation()
	if recResp.GetAnswer() != "I recommend these." {
		t.Errorf("answer = %q, want I recommend these.", recResp.GetAnswer())
	}
	if recResp.GetSessionId() != "sess-1" {
		t.Errorf("session_id = %q, want sess-1", recResp.GetSessionId())
	}
	if len(recResp.Books) != 1 {
		t.Fatalf("books = %d, want 1", len(recResp.Books))
	}
	if recResp.Books[0].GetBookId() != "b1" {
		t.Errorf("book_id = %q, want b1", recResp.Books[0].GetBookId())
	}
	if recResp.Source != aiv1.RecommendationSource_RECOMMENDATION_SOURCE_GENERATED {
		t.Errorf("source = %v, want SOURCE_GENERATED", recResp.Source)
	}
}

func TestRecommendUnauthenticated(t *testing.T) {
	svc := newSvc(t, &stubRec{}, &stubIdx{}, &stubStore{}, &stubCat{})
	srv := handler.NewGRPCServer(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

	_, err := srv.Recommend(context.Background(), &aiv1.RecommendRequest{Query: "q"})
	if err == nil {
		t.Fatal("expected error for unauthenticated request")
	}
	if got := status.Code(err); got != codes.Unauthenticated {
		t.Errorf("code = %v, want Unauthenticated", got)
	}
}

func TestRecommendEmptyQuery(t *testing.T) {
	svc := newSvc(t, &stubRec{}, &stubIdx{}, &stubStore{}, &stubCat{})
	srv := handler.NewGRPCServer(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

	_, err := srv.Recommend(withReader(context.Background()), &aiv1.RecommendRequest{Query: ""})
	if err == nil {
		t.Fatal("expected error for empty query")
	}
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}
// --- IngestBook ---

func TestIngestBookRequiresLibrarian(t *testing.T) {
	svc := newSvc(t, &stubRec{}, &stubIdx{}, &stubStore{}, &stubCat{})
	srv := handler.NewGRPCServer(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

	_, err := srv.IngestBook(withReader(context.Background()), &aiv1.IngestBookRequest{BookId: testBookID})
	if err == nil {
		t.Fatal("expected error for reader")
	}
	if got := status.Code(err); got != codes.PermissionDenied {
		t.Errorf("code = %v, want PermissionDenied", got)
	}
}

func TestIngestBookSuccess(t *testing.T) {
	cat := &stubCat{book: domain.CatalogBook{ID: testBookID, Title: "Clean Architecture"}}
	svc := newSvc(t, &stubRec{}, &stubIdx{}, &stubStore{}, cat)
	srv := handler.NewGRPCServer(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

	resp, err := srv.IngestBook(withPrincipal(context.Background()), &aiv1.IngestBookRequest{BookId: testBookID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetBookId() != testBookID {
		t.Errorf("book_id = %q, want %q", resp.GetBookId(), testBookID)
	}
	if resp.GetTitle() != "Clean Architecture" {
		t.Errorf("title = %q, want Clean Architecture", resp.GetTitle())
	}
}

func TestIngestBookNotFound(t *testing.T) {
	cat := &stubCat{err: domain.ErrBookNotFound}
	svc := newSvc(t, &stubRec{}, &stubIdx{}, &stubStore{}, cat)
	srv := handler.NewGRPCServer(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

	_, err := srv.IngestBook(withPrincipal(context.Background()), &aiv1.IngestBookRequest{BookId: testBookID})
	if err == nil {
		t.Fatal("expected error for not found")
	}
	if got := status.Code(err); got != codes.NotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

func TestIngestBookInvalidID(t *testing.T) {
	svc := newSvc(t, &stubRec{}, &stubIdx{}, &stubStore{}, &stubCat{})
	srv := handler.NewGRPCServer(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

	_, err := srv.IngestBook(withPrincipal(context.Background()), &aiv1.IngestBookRequest{BookId: "not-uuid"})
	if err == nil {
		t.Fatal("expected error for invalid ID")
	}
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}

// --- DeleteBook ---

func TestDeleteBookSuccess(t *testing.T) {
	st := &stubStore{delResp: domain.IndexDeletion{BookID: testBookID, Deleted: true, Chunks: 3}}
	svc := newSvc(t, &stubRec{}, &stubIdx{}, st, &stubCat{})
	srv := handler.NewGRPCServer(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

	resp, err := srv.DeleteBook(withPrincipal(context.Background()), &aiv1.DeleteBookRequest{BookId: testBookID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetDeleted() != true {
		t.Errorf("deleted = %v, want true", resp.GetDeleted())
	}
}

func TestDeleteBookPermissionDenied(t *testing.T) {
	svc := newSvc(t, &stubRec{}, &stubIdx{}, &stubStore{}, &stubCat{})
	srv := handler.NewGRPCServer(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

	_, err := srv.DeleteBook(withReader(context.Background()), &aiv1.DeleteBookRequest{BookId: testBookID})
	if err == nil {
		t.Fatal("expected error for reader")
	}
	if got := status.Code(err); got != codes.PermissionDenied {
		t.Errorf("code = %v, want PermissionDenied", got)
	}
}
// --- IndexStatus ---

func TestIndexStatusSuccess(t *testing.T) {
	st := &stubStore{stats: domain.IndexStats{Books: 42, Chunks: 128}}
	svc := newSvc(t, &stubRec{}, &stubIdx{}, st, &stubCat{})
	srv := handler.NewGRPCServer(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

	resp, err := srv.IndexStatus(withPrincipal(context.Background()), &aiv1.IndexStatusRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetIndexedBooks() != 42 {
		t.Errorf("indexed_books = %d, want 42", resp.GetIndexedBooks())
	}
	if resp.GetIndexedChunks() != 128 {
		t.Errorf("indexed_chunks = %d, want 128", resp.GetIndexedChunks())
	}
}

func TestIndexStatusPermissionDenied(t *testing.T) {
	svc := newSvc(t, &stubRec{}, &stubIdx{}, &stubStore{}, &stubCat{})
	srv := handler.NewGRPCServer(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

	_, err := srv.IndexStatus(withReader(context.Background()), &aiv1.IndexStatusRequest{})
	if err == nil {
		t.Fatal("expected error for reader")
	}
	if got := status.Code(err); got != codes.PermissionDenied {
		t.Errorf("code = %v, want PermissionDenied", got)
	}
}

// --- Error mapping ---

func TestMapErrorBackendUnavailable(t *testing.T) {
	svc := newSvc(t, &stubRec{err: domain.ErrBackendUnavailable}, &stubIdx{}, &stubStore{}, &stubCat{})
	srv := handler.NewGRPCServer(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

	_, err := srv.Recommend(withReader(context.Background()), &aiv1.RecommendRequest{Query: "q"})
	if err == nil {
		t.Fatal("expected error")
	}
	if got := status.Code(err); got != codes.Unavailable {
		t.Errorf("code = %v, want Unavailable", got)
	}
}

func TestMapErrorBackendRejected(t *testing.T) {
	svc := newSvc(t, &stubRec{err: domain.ErrBackendRejected}, &stubIdx{}, &stubStore{}, &stubCat{})
	srv := handler.NewGRPCServer(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

	_, err := srv.Recommend(withReader(context.Background()), &aiv1.RecommendRequest{Query: "q"})
	if err == nil {
		t.Fatal("expected error")
	}
	if got := status.Code(err); got != codes.Internal {
		t.Errorf("code = %v, want Internal", got)
	}
}

func TestMapErrorContextCanceled(t *testing.T) {
	// The stub recommender returns context.Canceled to simulate a canceled call.
	rec := &stubRec{err: context.Canceled}
	svc := newSvc(t, rec, &stubIdx{}, &stubStore{}, &stubCat{})
	srv := handler.NewGRPCServer(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

	_, err := srv.Recommend(withReader(context.Background()), &aiv1.RecommendRequest{Query: "q"})
	if err == nil {
		t.Fatal("expected error")
	}
	if got := status.Code(err); got != codes.Canceled {
		t.Errorf("code = %v, want Canceled", got)
	}
}

func TestMapErrorUnexpected(t *testing.T) {
	svc := newSvc(t, &stubRec{err: errors.New("unknown")}, &stubIdx{}, &stubStore{}, &stubCat{})
	srv := handler.NewGRPCServer(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

	_, err := srv.Recommend(withReader(context.Background()), &aiv1.RecommendRequest{Query: "q"})
	if err == nil {
		t.Fatal("expected error")
	}
	if got := status.Code(err); got != codes.Internal {
		t.Errorf("code = %v, want Internal", got)
	}
}
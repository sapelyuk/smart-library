// Package service implements the use cases of the AI service: answering a
// reader query through the RAG backend, and keeping the vector index in step
// with the catalog owned by book-service.
//
// The package talks to the outside world exclusively through the ports declared
// here — Recommender, Indexer, IndexStore, Catalog — so the n8n workflow, the
// pgvector store and book-service can each be replaced or faked without touching
// a use case. Authorization is a use-case concern, not a transport one: every
// method takes the caller as an explicit argument.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/sapelyuk/smart-library/services/ai-service/internal/domain"
)

// Recommender produces the answer to one validated query. The n8n workflow
// behind internal/n8n is the implementation; tests hand in a stub.
type Recommender interface {
	Recommend(ctx context.Context, query domain.Query) (domain.Answer, error)
}

// Indexer writes one catalogue record into the vector store: the text is chunked,
// embedded and stored under the book identifier, replacing what was there.
type Indexer interface {
	Ingest(ctx context.Context, book domain.CatalogBook) error
}

// IndexStore maintains the rows the retrieval step reads. Deleting is separate
// from Indexer because the pipeline that writes the chunks cannot remove a book
// the catalog already forgot: that path goes straight to the store.
type IndexStore interface {
	DeleteBook(ctx context.Context, bookID string) (domain.IndexDeletion, error)
	Stats(ctx context.Context) (domain.IndexStats, error)
}

// Catalog reads the bibliographic record of one book from its owner.
type Catalog interface {
	Book(ctx context.Context, bookID string) (domain.CatalogBook, error)
}

// Embedder turns one piece of text into the vector space of the index. The
// native retrieval path (ADR-0002, option C) needs it; while the n8n workflow
// owns the cycle, nothing in this service calls it today.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// BookIndex answers the retrieval step: the catalogue entries closest to a query
// vector.
type BookIndex interface {
	Search(ctx context.Context, embedding []float32, limit int) ([]domain.RecommendedBook, error)
}

// Config tunes the service.
type Config struct {
	// Logger receives the outcome of each use case; nil means slog.Default().
	Logger *slog.Logger
}

// Service orchestrates the RAG backend, the vector store and the catalog.
type Service struct {
	recommender Recommender
	indexer     Indexer
	store       IndexStore
	catalog     Catalog
	log         *slog.Logger
}

// New wires the service with its dependencies. All four are required: a service
// that can recommend but not index would drift away from the catalog silently.
func New(recommender Recommender, indexer Indexer, store IndexStore, catalog Catalog, cfg Config) (*Service, error) {
	if recommender == nil {
		return nil, errors.New("service: recommender is required")
	}

	if indexer == nil {
		return nil, errors.New("service: indexer is required")
	}

	if store == nil {
		return nil, errors.New("service: index store is required")
	}

	if catalog == nil {
		return nil, errors.New("service: catalog is required")
	}

	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}

	return &Service{
		recommender: recommender,
		indexer:     indexer,
		store:       store,
		catalog:     catalog,
		log:         log,
	}, nil
}

// RecommendInput is the raw input of the Recommend use case.
type RecommendInput struct {
	Query     string
	SessionID string
	Limit     int
}

// Recommend answers a reader query.
//
// Every authenticated caller may ask: the answer only ever contains titles the
// catalog holds, so there is no per-reader data to authorize here.
func (s *Service) Recommend(ctx context.Context, caller domain.Principal, input RecommendInput) (domain.Answer, error) {
	query, err := domain.NewQuery(input.Query, input.SessionID, input.Limit)
	if err != nil {
		return domain.Answer{}, err
	}

	answer, err := s.recommender.Recommend(ctx, query)
	if err != nil {
		return domain.Answer{}, fmt.Errorf("service: recommend: %w", err)
	}

	// A backend that drops the conversation token must not break the follow-up:
	// the token the caller sent is echoed back so a client can keep using it.
	if answer.SessionID == "" {
		answer.SessionID = query.SessionID
	}

	s.log.InfoContext(ctx, "recommendation produced",
		"reader", caller.UserID.String(),
		"books", len(answer.Books),
		"source", answer.Source.String(),
		"session_id", answer.SessionID)

	return answer, nil
}

// IngestBook reindexes one book on demand.
//
// The record is read back from book-service rather than taken from the request:
// the catalog owns the bibliographic data, and an index built from whatever a
// caller pastes would be an index nobody can trust.
func (s *Service) IngestBook(ctx context.Context, caller domain.Principal, rawBookID string) (domain.CatalogBook, error) {
	if err := caller.RequireLibrarian("reindexing a book"); err != nil {
		return domain.CatalogBook{}, err
	}

	return s.ingest(ctx, rawBookID)
}

// SyncBook is the ingest path of the catalog consumer, which has no principal:
// the event it processes is the authorization, because only the platform publishes
// the book.* routing keys.
func (s *Service) SyncBook(ctx context.Context, rawBookID string) (domain.CatalogBook, error) {
	return s.ingest(ctx, rawBookID)
}

func (s *Service) ingest(ctx context.Context, rawBookID string) (domain.CatalogBook, error) {
	bookID, err := domain.ParseBookID(rawBookID)
	if err != nil {
		return domain.CatalogBook{}, err
	}

	book, err := s.catalog.Book(ctx, bookID)
	if err != nil {
		return domain.CatalogBook{}, err
	}

	if err := book.Validate(); err != nil {
		return domain.CatalogBook{}, err
	}

	if err := s.indexer.Ingest(ctx, book); err != nil {
		return domain.CatalogBook{}, err
	}

	s.log.InfoContext(ctx, "book indexed", "book_id", book.ID, "title", book.Title)

	return book, nil
}

// DeleteBook drops one book from the vector index.
func (s *Service) DeleteBook(ctx context.Context, caller domain.Principal, rawBookID string) (domain.IndexDeletion, error) {
	if err := caller.RequireLibrarian("removing a book from the index"); err != nil {
		return domain.IndexDeletion{}, err
	}

	return s.remove(ctx, rawBookID)
}

// RemoveBook is the delete path of the catalog consumer.
func (s *Service) RemoveBook(ctx context.Context, rawBookID string) (domain.IndexDeletion, error) {
	return s.remove(ctx, rawBookID)
}

func (s *Service) remove(ctx context.Context, rawBookID string) (domain.IndexDeletion, error) {
	bookID, err := domain.ParseBookID(rawBookID)
	if err != nil {
		return domain.IndexDeletion{}, err
	}

	deletion, err := s.store.DeleteBook(ctx, bookID)
	if err != nil {
		return domain.IndexDeletion{}, err
	}

	deletion.BookID = bookID

	s.log.InfoContext(ctx, "book removed from the index",
		"book_id", bookID, "chunks", deletion.Chunks, "deleted", deletion.Deleted)

	return deletion, nil
}

// IndexStatus reports the size of the vector index.
//
// It is staff-only: the counts say how much of the catalog is exposed to
// retrieval, which is an operational figure rather than a reader's answer.
func (s *Service) IndexStatus(ctx context.Context, caller domain.Principal) (domain.IndexStats, error) {
	if err := caller.RequireLibrarian("reading the index status"); err != nil {
		return domain.IndexStats{}, err
	}

	return s.store.Stats(ctx)
}

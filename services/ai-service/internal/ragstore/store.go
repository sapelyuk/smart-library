// Package ragstore is the pgvector store the RAG cycle reads from.
//
// The store is written by the indexing workflow: the catalogue table `books` and
// the chunk table `book_chunks` come from rag/db/01-schema.sql, which the
// container of the store applies on first start. This package therefore owns no
// migrations — it owns the operations the service needs on top of that schema:
// retrieval by vector, deletion of one book together with its chunks, and the
// counters the status endpoint reports.
//
// The SQL is parameterized throughout: the vector travels as a bound halfvec
// literal and a book id as a bound text parameter, never as concatenated SQL.
package ragstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	// Register the pgx driver under the name "pgx".
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sapelyuk/smart-library/services/ai-service/internal/domain"
	"github.com/sapelyuk/smart-library/services/ai-service/internal/service"
)

// MaxSearchLimit bounds how many entries one retrieval call may return, whatever
// the caller asked for: the answer is a recommendation list, not a catalogue dump.
const MaxSearchLimit = 20

// Store reads and prunes the vector store.
type Store struct {
	db *sql.DB
}

var (
	_ service.BookIndex  = (*Store)(nil)
	_ service.IndexStore = (*Store)(nil)
)

// Open builds the pool of the vector store.
func Open(dsn string, maxOpenConns, maxIdleConns int, connMaxLifetime time.Duration) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("ragstore: open: %w", err)
	}

	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxIdleConns)
	db.SetConnMaxLifetime(connMaxLifetime)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("ragstore: ping: %w", err)
	}

	return db, nil
}

// NewStore wraps an opened pool. The caller owns the lifecycle of the pool.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// searchSQL returns the closest chunks of the store, collapsed to one row per
// book and ordered by relevance.
//
// The cast of the first parameter is deliberately bare (::halfvec): PostgreSQL
// takes the width from the literal, so a vector from a model of another width
// fails with a dimension error instead of silently matching nothing. A book is
// one recommendation however many of its chunks matched, so the collapse happens
// before the limit of the caller is applied.
const searchSQL = `
	WITH nearest AS (
		SELECT c.metadata->>'book_id'                    AS book_id,
		       coalesce(b.title, c.metadata->>'title')   AS title,
		       coalesce(b.author, c.metadata->>'author') AS author,
		       1 - (c.embedding <=> $1::halfvec)         AS similarity
		FROM book_chunks c
		LEFT JOIN books b ON b.book_id = c.metadata->>'book_id'
		WHERE c.embedding IS NOT NULL
		ORDER BY c.embedding <=> $1::halfvec
		LIMIT $2
	)
	SELECT book_id, title, author, similarity
	FROM (
		SELECT DISTINCT ON (book_id) book_id, title, author, similarity
		FROM nearest
		WHERE book_id IS NOT NULL
		ORDER BY book_id, similarity DESC
	) best
	ORDER BY similarity DESC
	LIMIT $3`

// Search returns the catalogue entries closest to the query vector, in relevance
// order.
func (s *Store) Search(ctx context.Context, embedding []float32, limit int) ([]domain.RecommendedBook, error) {
	if len(embedding) == 0 {
		return nil, errors.New("ragstore: embedding is empty")
	}

	if limit <= 0 {
		limit = domain.DefaultLimit
	}

	if limit > MaxSearchLimit {
		limit = MaxSearchLimit
	}

	// Candidate chunks are fetched generously so that collapsing per book still
	// leaves enough distinct titles to fill the page.
	candidates := limit * 5

	rows, err := s.db.QueryContext(ctx, searchSQL, vectorLiteral(embedding), candidates, limit)
	if err != nil {
		return nil, toStoreError(ctx, "search", err)
	}

	defer rows.Close()

	var found []domain.RecommendedBook

	for rows.Next() {
		var (
			book  domain.RecommendedBook
			score float64
		)

		if err := rows.Scan(&book.BookID, &book.Title, &book.Author, &score); err != nil {
			return nil, toStoreError(ctx, "scan search row", err)
		}

		found = append(found, book)
	}

	if err := rows.Err(); err != nil {
		return nil, toStoreError(ctx, "read search rows", err)
	}

	return found, nil
}

// DeleteBook removes a book and its chunks through delete_book of the schema.
//
// The SQL function is used rather than two DELETEs because book_chunks has no
// foreign key to books: dropping the catalogue row alone would leave searchable
// orphan chunks behind, which is the bug that function exists to prevent.
func (s *Store) DeleteBook(ctx context.Context, bookID string) (domain.IndexDeletion, error) {
	var (
		chunks  int64
		deleted bool
	)

	const query = `SELECT deleted_chunks, deleted_book FROM delete_book($1)`

	if err := s.db.QueryRowContext(ctx, query, bookID).Scan(&chunks, &deleted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// The function always answers with one row; no row means the schema was
			// never applied to this database.
			return domain.IndexDeletion{}, fmt.Errorf("%w: delete_book is missing, apply rag/db/01-schema.sql",
				domain.ErrIndexUnavailable)
		}

		return domain.IndexDeletion{}, toStoreError(ctx, "delete book", err)
	}

	return domain.IndexDeletion{BookID: bookID, Chunks: int(chunks), Deleted: deleted}, nil
}

// Stats reports how much of the catalogue the store holds.
func (s *Store) Stats(ctx context.Context) (domain.IndexStats, error) {
	const query = `
		SELECT (SELECT count(*) FROM books),
		       (SELECT count(*) FROM book_chunks WHERE metadata->>'book_id' IS NOT NULL)`

	var stats domain.IndexStats

	if err := s.db.QueryRowContext(ctx, query).Scan(&stats.Books, &stats.Chunks); err != nil {
		return domain.IndexStats{}, toStoreError(ctx, "read counters", err)
	}

	return stats, nil
}

// vectorLiteral renders the pgvector text form of an embedding: [0.1,0.2,...].
func vectorLiteral(embedding []float32) string {
	var builder strings.Builder

	builder.WriteByte('[')

	for i, value := range embedding {
		if i > 0 {
			builder.WriteByte(',')
		}

		// Shortest round-trip decimal: the column halves the precision anyway, and
		// a fixed %f would make the parameter of a 3072-wide vector several times
		// wider for no gain.
		builder.WriteString(strconv.FormatFloat(float64(value), 'g', -1, 32))
	}

	builder.WriteByte(']')

	return builder.String()
}

// toStoreError keeps the storage details out of the caller's view: a connection or
// a schema problem means the index is unavailable, which is not something the
// reader can act on. A canceled context travels through unchanged so a shutdown is
// not reported as an outage.
func toStoreError(ctx context.Context, what string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return fmt.Errorf("%w: %s: %s (%s)", domain.ErrIndexUnavailable, what, pgErr.Message, pgErr.Code)
	}

	return fmt.Errorf("%w: %s: %w", domain.ErrIndexUnavailable, what, err)
}

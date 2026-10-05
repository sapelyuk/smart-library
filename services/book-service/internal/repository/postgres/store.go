// Package postgres implements the storage contracts of the book service on top
// of PostgreSQL via pgx/v5/stdlib (database/sql driver).
//
// The package is the only place that knows SQL and the only one that imports a
// driver. pgx/v5/stdlib is chosen because it exposes the familiar *sql.DB
// interface while using the pgx wire protocol under the hood.
//
// A storage failure never reaches the service layer as a driver error: unique
// index violations are mapped back onto domain errors, a missing row onto
// domain.ErrNotFound. Everything else travels up wrapped and is reported as an
// internal error.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	// Register the pgx driver under the name "pgx".
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sapelyuk/smart-library/services/book-service/internal/domain"
	"github.com/sapelyuk/smart-library/services/book-service/internal/repository"
)

// Store is the PostgreSQL implementation of both repository.BookRepository and
// repository.CopyRepository.
type Store struct {
	db *sql.DB
}

var (
	_ repository.BookRepository = (*Store)(nil)
	_ repository.CopyRepository = (*Store)(nil)
)

// NewStore wraps an opened pool. The caller owns the lifecycle of the pool, the
// store never closes it.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Open builds the connection pool for the book service.
func Open(dsn string, maxOpenConns, maxIdleConns int, connMaxLifetime time.Duration) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: open: %w", err)
	}

	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxIdleConns)
	db.SetConnMaxLifetime(connMaxLifetime)

	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("postgres: ping: %w", err)
	}

	return db, nil
}

// ---------- BookRepository ----------

// bookColumns is the projection of one catalog entry.
const bookColumns = `id, isbn, title, author, publisher, published_year, created_at, updated_at`

// Create inserts a new book into the catalog.
// ISBN uniqueness is enforced by the unique index, not by a SELECT-then-INSERT
// sequence that would let two concurrent registrations through.
func (s *Store) Create(ctx context.Context, book *domain.Book) error {
	const query = `
		INSERT INTO books (id, isbn, title, author, publisher, published_year, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

	_, err := s.db.ExecContext(ctx, query,
		book.ID, book.ISBN.String(), book.Title, book.Author,
		book.Publisher, book.PublishedYear, book.CreatedAt, book.UpdatedAt,
	)

	return translateBookErr(err, book.ISBN)
}

// Update writes the mutable columns of a book.
func (s *Store) Update(ctx context.Context, book *domain.Book) error {
	const query = `
		UPDATE books
		SET isbn = $2, title = $3, author = $4, publisher = $5,
			published_year = $6, updated_at = $7
		WHERE id = $1`

	result, err := s.db.ExecContext(ctx, query,
		book.ID, book.ISBN.String(), book.Title, book.Author,
		book.Publisher, book.PublishedYear, book.UpdatedAt,
	)
	if err != nil {
		return translateBookErr(err, book.ISBN)
	}

	return touched(result, domain.ErrNotFound)
}

// Delete removes a book together with its copies.
//
// ON DELETE RESTRICT on book_copies blocks deleting a book that still has
// copies, so the copies are removed first inside the same transaction. The
// service layer already refuses to delete a book with active loans, so this
// cannot drop loan history silently; RESTRICT stays as the last line of defence
// against a concurrent insert between the two statements.
func (s *Store) Delete(ctx context.Context, id uuid.UUID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("postgres: delete begin: %w", err)
	}

	defer func() {
		_ = tx.Rollback()
	}()

	if _, err := tx.ExecContext(ctx, `DELETE FROM book_copies WHERE book_id = $1`, id); err != nil {
		return fmt.Errorf("postgres: delete copies: %w", err)
	}

	result, err := tx.ExecContext(ctx, `DELETE FROM books WHERE id = $1`, id)
	if err != nil {
		return translateBookDeleteErr(err)
	}

	if err := touched(result, domain.ErrNotFound); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("postgres: delete commit: %w", err)
	}

	return nil
}

// GetByID returns the book by its identifier.
func (s *Store) GetByID(ctx context.Context, id uuid.UUID) (*domain.Book, error) {
	query := `SELECT ` + bookColumns + ` FROM books WHERE id = $1`

	return s.queryBook(ctx, query, id)
}

// GetByISBN returns the book by its normalized ISBN.
func (s *Store) GetByISBN(ctx context.Context, isbn domain.ISBN) (*domain.Book, error) {
	query := `SELECT ` + bookColumns + ` FROM books WHERE isbn = $1`

	return s.queryBook(ctx, query, isbn.String())
}

// List returns a page of books matching the filter and the total count.
func (s *Store) List(ctx context.Context, filter domain.BookFilter) ([]*domain.Book, int, error) {
	var (
		conditions []string
		args       []any
	)

	if text := escapeLike(filter.Query); text != "" {
		pattern := "%" + text + "%"
		args = append(args, pattern)
		conditions = append(conditions, `(title ILIKE $1 OR author ILIKE $1 OR isbn::text ILIKE $1)`)
	}

	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	total, err := s.countBooks(ctx, where, args)
	if err != nil {
		return nil, 0, err
	}

	args = append(args, filter.Limit, filter.Offset)

	query := `SELECT ` + bookColumns + ` FROM books` + where +
		` ORDER BY created_at DESC, id DESC` +
		` LIMIT $` + strconv.Itoa(len(args)-1) +
		` OFFSET $` + strconv.Itoa(len(args))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres: list books: %w", err)
	}

	defer rows.Close()

	books := make([]*domain.Book, 0, 32)

	for rows.Next() {
		book, err := scanBook(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("postgres: list books: %w", err)
		}

		books = append(books, book)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("postgres: list books: %w", err)
	}

	return books, total, nil
}

// ---------- CopyRepository ----------

// copyColumns is the projection of one physical copy.
const copyColumns = `id, book_id, barcode, status, created_at`

// CreateCopy inserts a new physical copy.
func (s *Store) CreateCopy(ctx context.Context, copy *domain.Copy) error {
	// Verify the book exists before inserting (foreign key will also catch it,
	// but we want to return a domain error not a constraint violation).
	if _, err := s.GetByID(ctx, copy.BookID); err != nil {
		return err
	}

	const query = `
		INSERT INTO book_copies (id, book_id, barcode, status, created_at)
		VALUES ($1, $2, $3, $4, $5)`

	_, err := s.db.ExecContext(ctx, query,
		copy.ID, copy.BookID, copy.Barcode, string(copy.Status), copy.CreatedAt,
	)

	return translateCopyErr(err, copy.Barcode)
}

// GetCopyByID returns the copy by its identifier.
func (s *Store) GetCopyByID(ctx context.Context, id uuid.UUID) (*domain.Copy, error) {
	query := `SELECT ` + copyColumns + ` FROM book_copies WHERE id = $1`

	return s.queryCopy(ctx, query, id)
}

// ListByBook returns copies of the book ordered by registration time.
func (s *Store) ListByBook(ctx context.Context, bookID uuid.UUID) ([]*domain.Copy, error) {
	// Verify the book exists.
	if _, err := s.GetByID(ctx, bookID); err != nil {
		return nil, err
	}

	query := `SELECT ` + copyColumns + ` FROM book_copies WHERE book_id = $1 ORDER BY created_at ASC, id ASC`

	rows, err := s.db.QueryContext(ctx, query, bookID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list copies: %w", err)
	}

	defer rows.Close()

	copies := make([]*domain.Copy, 0, 8)

	for rows.Next() {
		copy, err := scanCopy(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: list copies: %w", err)
		}

		copies = append(copies, copy)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list copies: %w", err)
	}

	return copies, nil
}

// UpdateStatus changes the lifecycle state of a copy.
func (s *Store) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.CopyStatus) error {
	if !status.Valid() {
		return fmt.Errorf("%w: %s", domain.ErrInvalidCopyStatus, status)
	}

	const query = `UPDATE book_copies SET status = $2 WHERE id = $1`

	result, err := s.db.ExecContext(ctx, query, id, string(status))
	if err != nil {
		return fmt.Errorf("postgres: update copy status: %w", err)
	}

	return touched(result, domain.ErrCopyNotFound)
}

// Stats aggregates the inventory of one book.
func (s *Store) Stats(ctx context.Context, bookID uuid.UUID) (domain.CopyStats, error) {
	// Verify the book exists.
	if _, err := s.GetByID(ctx, bookID); err != nil {
		return domain.CopyStats{}, err
	}

	const query = `
		SELECT
			COUNT(*)::int,
			COUNT(*) FILTER (WHERE status = 'AVAILABLE')::int,
			COUNT(*) FILTER (WHERE status = 'ON_LOAN')::int
		FROM book_copies
		WHERE book_id = $1`

	var stats domain.CopyStats

	err := s.db.QueryRowContext(ctx, query, bookID).Scan(
		&stats.Total, &stats.Available, &stats.OnLoan,
	)
	if err != nil {
		return domain.CopyStats{}, fmt.Errorf("postgres: copy stats: %w", err)
	}

	return stats, nil
}

// AcquireAvailable atomically marks the oldest available copy as ON_LOAN and
// returns it. Uses SELECT ... FOR UPDATE SKIP LOCKED so that concurrent callers
// never receive the same copy. FOR SHARE on the book row prevents deleting the
// book in the same transaction.
func (s *Store) AcquireAvailable(ctx context.Context, bookID uuid.UUID) (*domain.Copy, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelReadCommitted,
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: acquire begin: %w", err)
	}

	defer func() {
		_ = tx.Rollback()
	}()

	// FOR SHARE on the book row: prevents deleting the book while acquiring.
	var bookExists bool

	err = tx.QueryRowContext(ctx, `SELECT true FROM books WHERE id = $1 FOR SHARE`, bookID).Scan(&bookExists)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, bookNotFound(bookID)
		}

		return nil, fmt.Errorf("postgres: acquire lock book: %w", err)
	}

	// SELECT ... FOR UPDATE SKIP LOCKED: lock the oldest available copy, skip
	// copies already locked by another concurrent transaction.
	const selectQuery = `
		SELECT ` + copyColumns + `
		FROM book_copies
		WHERE book_id = $1 AND status = 'AVAILABLE'
		ORDER BY created_at ASC, id ASC
		LIMIT 1
		FOR UPDATE SKIP LOCKED`

	row := tx.QueryRowContext(ctx, selectQuery, bookID)

	copy, err := scanCopy(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: book %s", domain.ErrNoAvailableCopies, bookID)
		}

		return nil, fmt.Errorf("postgres: acquire select: %w", err)
	}

	// Mark the copy as ON_LOAN.
	const updateQuery = `UPDATE book_copies SET status = 'ON_LOAN' WHERE id = $1`

	if _, err := tx.ExecContext(ctx, updateQuery, copy.ID); err != nil {
		return nil, fmt.Errorf("postgres: acquire update: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("postgres: acquire commit: %w", err)
	}

	copy.Status = domain.CopyStatusOnLoan

	return copy, nil
}

// ---------- Internal helpers ----------

// queryBook runs one SELECT of the books projection.
func (s *Store) queryBook(ctx context.Context, query string, args ...any) (*domain.Book, error) {
	book, err := scanBook(s.db.QueryRowContext(ctx, query, args...))

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, domain.ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("postgres: query book: %w", err)
	default:
		return book, nil
	}
}

// queryCopy runs one SELECT of the copies projection.
func (s *Store) queryCopy(ctx context.Context, query string, args ...any) (*domain.Copy, error) {
	copy, err := scanCopy(s.db.QueryRowContext(ctx, query, args...))

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, domain.ErrCopyNotFound
	case err != nil:
		return nil, fmt.Errorf("postgres: query copy: %w", err)
	default:
		return copy, nil
	}
}

// countBooks applies the filter of a listing to the aggregate.
func (s *Store) countBooks(ctx context.Context, where string, args []any) (int, error) {
	query := `SELECT count(*) FROM books` + where

	var total int

	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("postgres: count books: %w", err)
	}

	return total, nil
}

// scanner is the part of *sql.Row and *sql.Rows the mappers depend on.
type scanner interface {
	Scan(dest ...any) error
}

// scanBook reads one row of the books table into an entity.
func scanBook(row scanner) (*domain.Book, error) {
	var book domain.Book

	err := row.Scan(
		&book.ID, &book.ISBN, &book.Title, &book.Author,
		&book.Publisher, &book.PublishedYear, &book.CreatedAt, &book.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	return &book, nil
}

// scanCopy reads one row of the book_copies table into an entity.
func scanCopy(row scanner) (*domain.Copy, error) {
	var (
		copy   domain.Copy
		status string
	)

	err := row.Scan(
		&copy.ID, &copy.BookID, &copy.Barcode, &status, &copy.CreatedAt,
	)
	if err != nil {
		return nil, err
	}

	copy.Status = domain.CopyStatus(status)

	return &copy, nil
}

// translateBookErr maps storage failures related to books onto domain errors.
func translateBookErr(err error, isbn domain.ISBN) error {
	if err == nil {
		return nil
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return fmt.Errorf("postgres: %w", err)
	}

	switch pgErr.Code {
	case "23505": // unique_violation
		if strings.Contains(pgErr.ConstraintName, "isbn") {
			return fmt.Errorf("%w: %s", domain.ErrISBNAlreadyExists, isbn)
		}

		return fmt.Errorf("postgres: unique violation on %s: %w", pgErr.ConstraintName, err)
	default:
		return fmt.Errorf("postgres: %s: %w", pgErr.Code, err)
	}
}

// translateBookDeleteErr maps FK violation from ON DELETE RESTRICT onto a
// domain error about active loans.
func translateBookDeleteErr(err error) error {
	if err == nil {
		return nil
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return fmt.Errorf("postgres: %w", err)
	}

	switch pgErr.Code {
	case "23503": // foreign_key_violation (RESTRICT)
		return domain.ErrBookHasActiveLoans
	default:
		return fmt.Errorf("postgres: %s: %w", pgErr.Code, err)
	}
}

// translateCopyErr maps storage failures related to copies onto domain errors.
func translateCopyErr(err error, barcode string) error {
	if err == nil {
		return nil
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return fmt.Errorf("postgres: %w", err)
	}

	switch pgErr.Code {
	case "23505": // unique_violation
		if strings.Contains(pgErr.ConstraintName, "barcode") {
			return fmt.Errorf("%w: %s", domain.ErrBarcodeAlreadyExists, barcode)
		}

		return fmt.Errorf("postgres: unique violation on %s: %w", pgErr.ConstraintName, err)
	case "23503": // foreign_key_violation
		return domain.ErrNotFound
	default:
		return fmt.Errorf("postgres: %s: %w", pgErr.Code, err)
	}
}

// touched checks that a write matched the row it addressed.
func touched(result sql.Result, notFound error) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres: rows affected: %w", err)
	}

	if rows == 0 {
		return notFound
	}

	return nil
}

// escapeLike neutralises the wildcards of a free text filter.
func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

	return replacer.Replace(strings.TrimSpace(value))
}

func bookNotFound(id uuid.UUID) error {
	return fmt.Errorf("%w: %s", domain.ErrNotFound, id)
}

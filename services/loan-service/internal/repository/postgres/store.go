// Package postgres implements the storage contracts of the loan service on top
// of PostgreSQL via pgx/v5/stdlib (database/sql driver).
//
// The package is the only place that knows SQL and the only one that imports a
// driver. A storage failure never reaches the service layer as a driver error:
// a unique index violation is mapped back onto a domain error, a missing row
// onto domain.ErrNotFound. Everything else travels up wrapped and is reported
// as an internal error.
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

	"github.com/sapelyuk/smart-library/services/loan-service/internal/domain"
	"github.com/sapelyuk/smart-library/services/loan-service/internal/repository"
)

// Store is the PostgreSQL implementation of repository.LoanRepository.
type Store struct {
	db *sql.DB
}

var _ repository.LoanRepository = (*Store)(nil)

// NewStore wraps an opened pool. The caller owns the lifecycle of the pool, the
// store never closes it.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Open builds the connection pool for the loan service.
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

// loanColumns is the projection of one loan.
const loanColumns = `id, reader_id, book_id, copy_id, borrowed_at, due_at, returned_at, status`

// Create inserts a new loan.
//
// The partial unique index on copy_id (WHERE returned_at IS NULL) is what
// guarantees a copy is never out on two loans at once; two concurrent borrows
// of the same copy cannot both succeed.
func (s *Store) Create(ctx context.Context, loan *domain.Loan) error {
	const query = `
		INSERT INTO loans (id, reader_id, book_id, copy_id, borrowed_at, due_at, returned_at, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

	_, err := s.db.ExecContext(ctx, query,
		loan.ID, loan.ReaderID, loan.BookID, loan.CopyID,
		loan.BorrowedAt, loan.DueAt, loan.ReturnedAt, string(loan.Status),
	)

	return translateLoanErr(err)
}

// GetByID returns the loan by its identifier.
func (s *Store) GetByID(ctx context.Context, id uuid.UUID) (*domain.Loan, error) {
	query := `SELECT ` + loanColumns + ` FROM loans WHERE id = $1`

	loan, err := scanLoan(s.db.QueryRowContext(ctx, query, id))

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, domain.ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("postgres: query loan: %w", err)
	default:
		return loan, nil
	}
}

// Update writes the mutable columns of a loan.
func (s *Store) Update(ctx context.Context, loan *domain.Loan) error {
	const query = `
		UPDATE loans
		SET due_at = $2, returned_at = $3, status = $4
		WHERE id = $1`

	result, err := s.db.ExecContext(ctx, query,
		loan.ID, loan.DueAt, loan.ReturnedAt, string(loan.Status),
	)
	if err != nil {
		return translateLoanErr(err)
	}

	return touched(result, domain.ErrNotFound)
}

// List returns a page of loans matching the filter and the total count.
func (s *Store) List(ctx context.Context, filter domain.LoanFilter) ([]*domain.Loan, int, error) {
	var (
		conditions []string
		args       []any
	)

	if filter.ReaderID != nil {
		args = append(args, *filter.ReaderID)
		conditions = append(conditions, "reader_id = $"+strconv.Itoa(len(args)))
	}

	if filter.BookID != nil {
		args = append(args, *filter.BookID)
		conditions = append(conditions, "book_id = $"+strconv.Itoa(len(args)))
	}

	switch filter.Status {
	case domain.StatusActive:
		conditions = append(conditions, "returned_at IS NULL AND due_at >= now()")
	case domain.StatusOverdue:
		conditions = append(conditions, "returned_at IS NULL AND due_at < now()")
	case domain.StatusReturned:
		conditions = append(conditions, "returned_at IS NOT NULL")
	}

	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	total, err := s.countLoans(ctx, where, args)
	if err != nil {
		return nil, 0, err
	}

	args = append(args, filter.Limit, filter.Offset)

	query := `SELECT ` + loanColumns + ` FROM loans` + where +
		` ORDER BY borrowed_at DESC, id DESC` +
		` LIMIT $` + strconv.Itoa(len(args)-1) +
		` OFFSET $` + strconv.Itoa(len(args))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres: list loans: %w", err)
	}

	defer rows.Close()

	loans := make([]*domain.Loan, 0, 32)

	for rows.Next() {
		loan, err := scanLoan(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("postgres: list loans: %w", err)
		}

		loans = append(loans, loan)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("postgres: list loans: %w", err)
	}

	return loans, total, nil
}

// countLoans applies the filter of a listing to the aggregate.
func (s *Store) countLoans(ctx context.Context, where string, args []any) (int, error) {
	query := `SELECT count(*) FROM loans` + where

	var total int

	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("postgres: count loans: %w", err)
	}

	return total, nil
}

// scanner is the part of *sql.Row and *sql.Rows the mappers depend on.
type scanner interface {
	Scan(dest ...any) error
}

// scanLoan reads one row of the loans table into an entity.
func scanLoan(row scanner) (*domain.Loan, error) {
	var (
		loan       domain.Loan
		returnedAt sql.NullTime
		status     string
	)

	err := row.Scan(
		&loan.ID, &loan.ReaderID, &loan.BookID, &loan.CopyID,
		&loan.BorrowedAt, &loan.DueAt, &returnedAt, &status,
	)
	if err != nil {
		return nil, err
	}

	if returnedAt.Valid {
		returned := returnedAt.Time
		loan.ReturnedAt = &returned
	}

	loan.Status = domain.LoanStatus(status)

	return &loan, nil
}

// translateLoanErr maps storage failures related to loans onto domain errors.
func translateLoanErr(err error) error {
	if err == nil {
		return nil
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return fmt.Errorf("postgres: %w", err)
	}

	switch pgErr.Code {
	case "23505": // unique_violation
		if strings.Contains(pgErr.ConstraintName, "active_copy") {
			return domain.ErrCopyAlreadyOnLoan
		}

		return fmt.Errorf("postgres: unique violation on %s: %w", pgErr.ConstraintName, err)
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

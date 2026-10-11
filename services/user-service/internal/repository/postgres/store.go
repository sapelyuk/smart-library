// Package postgres implements the storage contracts of the user service on top
// of PostgreSQL via pgx/v5/stdlib (database/sql driver).
//
// The package is the only place that knows SQL and the only one that imports a
// driver. pgx/v5/stdlib is chosen because it exposes the familiar *sql.DB
// interface while using the pgx wire protocol under the hood; it supports
// binary parameters, COPY and LISTEN/NOTIFY and has an active pool implementation.
//
// A storage failure never reaches the service layer as a driver error: the
// unique index that rejects a second registration of the same address is mapped
// back onto domain.ErrEmailAlreadyExists, a missing row onto
// domain.ErrUserNotFound. Everything else travels up wrapped and is reported as
// an internal error, because guessing at it in the transport would be wrong.
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
	_ "github.com/jackc/pgx/v5/stdlib" // registers the pgx database/sql driver

	"github.com/sapelyuk/smart-library/services/user-service/internal/domain"
	"github.com/sapelyuk/smart-library/services/user-service/internal/repository"
)

// Store is the PostgreSQL implementation of repository.UserRepository.
type Store struct {
	db *sql.DB
}

var _ repository.UserRepository = (*Store)(nil)

// NewStore wraps an opened pool. The caller owns the lifecycle of the pool, the
// store never closes it.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Open builds the pool of the service. The limits are parameters rather than
// constants because the right numbers depend on how many instances of the
// service run against the same server.
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

// userColumns is the projection of one account.
const userColumns = `id, email, password_hash, full_name, phone, role, status,
	created_at, updated_at, last_login_at`

// Create inserts an account.
//
// The email uniqueness is enforced by the index, not by a SELECT before the
// INSERT: the check-then-write sequence would let two concurrent registrations
// of the same address through.
func (s *Store) Create(ctx context.Context, user *domain.User) error {
	const query = `
		INSERT INTO users (id, email, password_hash, full_name, phone, role, status,
			created_at, updated_at, last_login_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

	_, err := s.db.ExecContext(ctx, query,
		user.ID, user.Email.String(), user.PasswordHash, user.FullName, user.Phone,
		string(user.Role), string(user.Status),
		user.CreatedAt, user.UpdatedAt, nullableTime(user.LastLogin),
	)

	return translate(err, user.Email)
}

// Update writes the mutable columns of an account.
func (s *Store) Update(ctx context.Context, user *domain.User) error {
	const query = `
		UPDATE users
		SET email = $2, password_hash = $3, full_name = $4, phone = $5, role = $6,
			status = $7, updated_at = $8, last_login_at = $9
		WHERE id = $1`

	result, err := s.db.ExecContext(ctx, query,
		user.ID, user.Email.String(), user.PasswordHash, user.FullName, user.Phone,
		string(user.Role), string(user.Status), user.UpdatedAt, nullableTime(user.LastLogin),
	)
	if err != nil {
		return translate(err, user.Email)
	}

	return touched(result, domain.ErrUserNotFound)
}

// GetByID returns the account with the given identifier.
func (s *Store) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	query := `SELECT ` + userColumns + ` FROM users WHERE id = $1`

	return s.queryUser(ctx, query, id)
}

// GetByEmail returns the account that owns the given address.
func (s *Store) GetByEmail(ctx context.Context, email domain.Email) (*domain.User, error) {
	query := `SELECT ` + userColumns + ` FROM users WHERE email = $1`

	return s.queryUser(ctx, query, email.String())
}

// List returns one page of accounts and the total number of rows the filter
// matches.
//
// Counting and paging are two queries. They are not wrapped in a transaction on
// purpose: a listing that is one request stale is harmless, while a transaction
// held open across both round trips costs a connection for no benefit.
func (s *Store) List(ctx context.Context, filter domain.UserFilter) ([]*domain.User, int, error) {
	var (
		conditions []string
		args       []any
	)

	if text := escapeLike(filter.Query); text != "" {
		pattern := "%" + text + "%"
		args = append(args, pattern)
		conditions = append(conditions, `(full_name ILIKE $1 OR email ILIKE $1 OR phone ILIKE $1)`)
	}

	if filter.Role != "" {
		args = append(args, string(filter.Role))
		conditions = append(conditions, `role = $`+strconv.Itoa(len(args)))
	}

	if filter.Status != "" {
		args = append(args, string(filter.Status))
		conditions = append(conditions, `status = $`+strconv.Itoa(len(args)))
	}

	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	total, err := s.count(ctx, where, args)
	if err != nil {
		return nil, 0, err
	}

	args = append(args, filter.Limit, filter.Offset)

	query := `SELECT ` + userColumns + ` FROM users` + where +
		` ORDER BY created_at DESC, id DESC` +
		` LIMIT $` + strconv.Itoa(len(args)-1) +
		` OFFSET $` + strconv.Itoa(len(args))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres: list users: %w", err)
	}

	defer rows.Close()

	users := make([]*domain.User, 0, listPageHint)

	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("postgres: list users: %w", err)
		}

		users = append(users, user)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("postgres: list users: %w", err)
	}

	return users, total, nil
}

// listPageHint only sizes the slice of a listing, it is not a limit.
const listPageHint = 32

// count applies the filter of a listing to the aggregate.
func (s *Store) count(ctx context.Context, where string, args []any) (int, error) {
	query := `SELECT count(*) FROM users` + where

	var total int

	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("postgres: count users: %w", err)
	}

	return total, nil
}

// queryUser runs one SELECT of the user projection.
func (s *Store) queryUser(ctx context.Context, query string, args ...any) (*domain.User, error) {
	user, err := scanUser(s.db.QueryRowContext(ctx, query, args...))

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, domain.ErrUserNotFound
	case err != nil:
		return nil, fmt.Errorf("postgres: query user: %w", err)
	default:
		return user, nil
	}
}

// scanner is the part of *sql.Row and *sql.Rows the mappers depend on, which
// keeps one mapper serve a single row and a result set alike.
type scanner interface {
	Scan(dest ...any) error
}

// scanUser reads one row of the users table into an entity.
func scanUser(row scanner) (*domain.User, error) {
	var (
		user      domain.User
		email     string
		role      string
		status    string
		lastLogin sql.NullTime
	)

	err := row.Scan(
		&user.ID, &email, &user.PasswordHash, &user.FullName, &user.Phone,
		&role, &status, &user.CreatedAt, &user.UpdatedAt, &lastLogin,
	)
	if err != nil {
		return nil, err
	}

	// The stored values are taken as they are. Re-running the registration rules
	// over a row written years ago would make the service unable to read its own
	// history after a policy change.
	user.Email = domain.Email(email)

	if user.Role, err = domain.ParseRole(role); err != nil {
		return nil, fmt.Errorf("postgres: user %s: %w", user.ID, err)
	}

	if user.Status, err = domain.ParseStatus(status); err != nil {
		return nil, fmt.Errorf("postgres: user %s: %w", user.ID, err)
	}

	if lastLogin.Valid {
		user.MarkLogin(lastLogin.Time)
	}

	return &user, nil
}

// translate maps the storage failures the service has an opinion about onto
// domain errors.
func translate(err error, email domain.Email) error {
	if err == nil {
		return nil
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return fmt.Errorf("postgres: %w", err)
	}

	switch pgErr.Code {
	case "23505": // unique_violation
		// users_email_key is the only unique index an account write can hit.
		if strings.Contains(pgErr.ConstraintName, "email") {
			return fmt.Errorf("%w: %s", domain.ErrEmailAlreadyExists, email)
		}

		return fmt.Errorf("postgres: unique violation on %s: %w", pgErr.ConstraintName, err)
	case "23503": // foreign_key_violation
		// sessions_user_id_fkey is the only foreign key of the schema.
		return domain.ErrUserNotFound
	default:
		return fmt.Errorf("postgres: %s: %w", pgErr.Code, err)
	}
}

// touched checks that a write matched the row it addressed, so that updating an
// account deleted in between cannot pass as success.
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

// nullableTime stores a zero time as NULL instead of year one, so that the
// column reads as "never happened" in psql.
func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}

	return value
}

// escapeLike neutralises the wildcards of a free text filter: a search for
// "100%" should match the literal text, not the whole table.
func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

	return replacer.Replace(strings.TrimSpace(value))
}

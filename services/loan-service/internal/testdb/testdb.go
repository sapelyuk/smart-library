// Package testdb provisions the PostgreSQL database used by the loan service
// tests.
//
// Every test that touches storage runs against a real PostgreSQL instance
// instead of a fake. The instance is located through POSTGRES_TEST_DSN; when the
// variable is not set the calling test is skipped, which keeps `go test ./...`
// usable on a machine without a database while CI runs the full suite.
//
// A single connection pool is shared by the tests of one process. A session
// level advisory lock, held for the lifetime of the process, serialises the
// packages go test runs in parallel so migrations and TRUNCATE never race.
package testdb

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	// Register the pgx driver under the name "pgx".
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sapelyuk/smart-library/pkg/migrate"
	"github.com/sapelyuk/smart-library/services/loan-service/migrations"
)

// dsnEnv is the environment variable holding the test database DSN.
const dsnEnv = "POSTGRES_TEST_DSN"

// lockKey identifies the advisory lock that serialises tests across packages.
const lockKey int64 = 0x4C4F414E // "LOAN"

var (
	setupOnce sync.Once
	pool      *sql.DB
	setupErr  error
)

// New returns a connection pool with the loan service schema applied and the
// loans table emptied. It skips the test when POSTGRES_TEST_DSN is not set.
//
// The returned pool is shared by all tests of the process; do not close it.
func New(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s is not set, skipping PostgreSQL test", dsnEnv)
	}

	setupOnce.Do(func() {
		setupErr = setup(dsn)
	})

	if setupErr != nil {
		t.Fatalf("testdb: setup: %v", setupErr)
	}

	Reset(t, pool)

	return pool
}

// setup opens the shared pool, takes the advisory lock and applies migrations.
func setup(dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	ctx := context.Background()

	// Hold the lock on a dedicated connection for the whole process: it must
	// not be returned to the pool while the lock is held, and go test may run
	// several packages concurrently.
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return err
	}

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
		_ = conn.Close()
		_ = db.Close()
		return err
	}

	if _, err := migrate.Apply(ctx, db, migrations.FS, ".", discardLogger()); err != nil {
		_ = conn.Close()
		_ = db.Close()
		return err
	}

	pool = db

	return nil
}

// Reset empties the loans table. Tests call it through New; it is exported for
// tests that need a clean state between subtests.
func Reset(t *testing.T, db *sql.DB) {
	t.Helper()

	const truncate = `TRUNCATE loans RESTART IDENTITY CASCADE`

	if _, err := db.ExecContext(context.Background(), truncate); err != nil {
		t.Fatalf("testdb: truncate: %v", err)
	}
}

// discardLogger keeps the migration runner silent during tests.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

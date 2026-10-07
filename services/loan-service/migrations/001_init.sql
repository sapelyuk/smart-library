-- 001_init.sql — initial schema of the Loan Service database.
-- Applied by pkg/migrate on service startup when LOAN_SERVICE_DB_MIGRATE=true.
--
-- Design decisions:
--   * the table owns the lending history and stores reader_id, book_id and
--     copy_id as plain UUIDs without foreign keys: those rows live in the
--     databases of user-service and book-service (database-per-service), and a
--     cross-database FK is not a thing;
--   * status is stored as ACTIVE or RETURNED only. OVERDUE is a function of
--     due_at and returned_at, so persisting it would only need a background job
--     to stay true;
--   * a partial unique index keeps a single copy from being out twice at the
--     same time while still allowing the full history of previous loans;
--   * gen_random_uuid() is built into PG 13+, no pgcrypto needed.

CREATE TABLE loans (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    reader_id   UUID        NOT NULL,
    book_id     UUID        NOT NULL,
    copy_id     UUID        NOT NULL,
    borrowed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    due_at      TIMESTAMPTZ NOT NULL,
    returned_at TIMESTAMPTZ,
    status      VARCHAR(16) NOT NULL DEFAULT 'ACTIVE'
                CHECK (status IN ('ACTIVE', 'RETURNED')),
    CONSTRAINT loans_due_after_borrowed CHECK (due_at > borrowed_at)
);

-- Supports "loans of this reader" listings, newest first.
CREATE INDEX loans_reader_idx ON loans (reader_id, borrowed_at DESC);

-- Supports "loans of this book" listings.
CREATE INDEX loans_book_idx ON loans (book_id);

-- Supports the overdue sweep: only active loans have a due date worth scanning.
CREATE INDEX loans_overdue_idx ON loans (due_at) WHERE returned_at IS NULL;

-- A physical copy cannot be out on two loans at once. Returned loans keep
-- their copy_id, so the constraint is partial.
CREATE UNIQUE INDEX loans_active_copy_idx ON loans (copy_id) WHERE returned_at IS NULL;

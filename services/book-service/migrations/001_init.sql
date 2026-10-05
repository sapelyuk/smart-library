-- 001_init.sql — initial schema of the Book Service database.
-- Applied by pkg/migrate on service startup when BOOK_SERVICE_DB_MIGRATE=true.
--
-- Design decisions:
--   * citext for ISBN: 978-5-... and 9785... are the same book;
--   * ON DELETE RESTRICT on book_copies: the contract promises to keep loan
--     history, so deleting a book with copies must fail at the storage level;
--   * gen_random_uuid() is built into PG 13+, no pgcrypto needed;
--   * ICU collation "ru-RU" ensures ILIKE and ORDER BY work correctly for
--     Cyrillic text (the "C" collation treats bytes, not characters).

-- Case-insensitive text for ISBN normalization.
CREATE EXTENSION IF NOT EXISTS citext;

-- ICU collation for correct Cyrillic search and sorting.
-- Requires PostgreSQL 15+ compiled with ICU support (standard in PG 17).
-- The name is quoted so it keeps the mixed case it is referenced with below.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_collation WHERE collname = 'ru_RU_icu'
    ) THEN
        CREATE COLLATION "ru_RU_icu" (
            provider = icu,
            locale   = 'ru-RU',
            deterministic = true
        );
    END IF;
END
$$;

CREATE TABLE books (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    isbn           citext        NOT NULL,
    title          TEXT          NOT NULL COLLATE "ru_RU_icu",
    author         TEXT          NOT NULL COLLATE "ru_RU_icu",
    publisher      TEXT          NOT NULL DEFAULT '',
    published_year INTEGER       NOT NULL CHECK (published_year BETWEEN 1445 AND 2300),
    created_at     TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ   NOT NULL DEFAULT now(),
    CONSTRAINT books_isbn_key UNIQUE (isbn)
);

CREATE INDEX books_title_idx ON books USING gin (to_tsvector('russian', title));
CREATE INDEX books_author_idx ON books USING gin (to_tsvector('russian', author));

CREATE TABLE book_copies (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    book_id    UUID        NOT NULL REFERENCES books (id) ON DELETE RESTRICT,
    barcode    VARCHAR(64) NOT NULL,
    status     VARCHAR(16) NOT NULL DEFAULT 'AVAILABLE'
               CHECK (status IN ('AVAILABLE', 'ON_LOAN', 'LOST', 'MAINTENANCE')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT book_copies_barcode_key UNIQUE (barcode)
);

-- Supports "give me the first available copy of this book" lookups.
CREATE INDEX book_copies_book_status_idx ON book_copies (book_id, status, created_at);

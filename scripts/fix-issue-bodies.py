#!/usr/bin/env python3
"""Step E: M3 bindings, M5 DDL sync, n1 typo, #106 Context link.

Uses str.replace only.  Idempotent.  Applies via gh issue edit --body-file.
"""

import json
import subprocess
import sys
import tempfile

# Each tuple: (issue_number, old_text, new_text)
REPLACEMENTS = [
    # === M3: bindings in #92 (line 71 and line 20 in body) ===
    (92, "bindings `book.#`, `rating.#`, `signal.#`, `loan.#`",
     "bindings `book.#`, `loan.#`, `ai.#`, `reading.#`"),
    (92, "key `book.#`, `rating.#`, `signal.#`, `loan.#`",
     "key `book.#`, `loan.#`, `ai.#`, `reading.#`"),
    (92, "bindings `book.#`, `loan.#`, `signal.#`, `loan.#`",
     "bindings `book.#`, `loan.#`, `ai.#`, `reading.#`"),

    # === M5: #90 wishlist DDL ===
    (90, "таблица `wishlist_items` (DDL ниже)",
     "таблица `signals.wishlist` (DDL ниже)"),
    (90, "CREATE TABLE wishlist_items (",
     "CREATE TABLE signals.wishlist ("),
    (90, "user_id    UUID NOT NULL,",
     "reader_id  UUID NOT NULL,"),
    (90, "book_id    UUID NOT NULL REFERENCES books (id) ON DELETE CASCADE,",
     "book_id    UUID NOT NULL REFERENCES books (id) ON DELETE CASCADE,"),
    (90, "CONSTRAINT wishlist_items_user_book_key UNIQUE (tenant_id, user_id, book_id)",
     "CONSTRAINT wishlist_unique_reader_book UNIQUE (tenant_id, reader_id, book_id)"),
    (90, "CREATE INDEX wishlist_items_user_idx ON wishlist_items (tenant_id, user_id, created_at DESC);",
     "CREATE INDEX wishlist_reader_idx ON signals.wishlist (tenant_id, reader_id, added_at DESC);"),
    (90, "уникальность `(tenant_id,user_id,book_id)`",
     "уникальность `(tenant_id,reader_id,book_id)`"),
    (90, "`user_id` — UUID из сессии user-service",
     "`reader_id` — UUID из сессии user-service"),
    (90, "`book_id`, `user_id`, `tenant_id`",
     "`book_id`, `reader_id`, `tenant_id`"),
    (90, "`04-data-model.md` (таблица `wishlist_items`, `tenant_id`)",
     "`04-data-model.md` (таблица `wishlist`, `tenant_id`)"),
    (90, "created_at TIMESTAMPTZ NOT NULL DEFAULT now(),",
     "added_at     TIMESTAMPTZ NOT NULL DEFAULT now(),\n      notified_at  TIMESTAMPTZ,"),

    # === M5: #91 ratings DDL ===
    (91, "CREATE TABLE ratings (",
     "CREATE TABLE signals.ratings ("),
    (91, "user_id    UUID NOT NULL,",
     "reader_id  UUID NOT NULL,"),
    (91, "value      SMALLINT NOT NULL CHECK (value BETWEEN 1 AND 5),",
     "score      SMALLINT NOT NULL CHECK (score BETWEEN 1 AND 5),\n      review     TEXT NOT NULL DEFAULT '',"),
    (91, "CONSTRAINT ratings_user_book_key UNIQUE (tenant_id, user_id, book_id)",
     "CONSTRAINT ratings_unique_reader_book UNIQUE (tenant_id, reader_id, book_id)"),
    (91, "CREATE INDEX ratings_book_idx ON ratings (book_id);",
     "CREATE INDEX ratings_book_idx ON signals.ratings (tenant_id, book_id);"),
    (91, "`ROUND(AVG(value)::numeric, 2)`",
     "`ROUND(AVG(score)::numeric, 2)`"),
    (91, "RateBook(RateBookRequest{book_id, value}) returns (RateBookResponse{value})",
     "RateBook(RateBookRequest{book_id, score}) returns (RateBookResponse{score})"),
    (91, "`{\"value\":5}` → 200 `{\"value\":5}`; без токена → 401; `{\"value\":9}`",
     "`{\"score\":5}` → 200 `{\"score\":5}`; без токена → 401; `{\"score\":9}`"),
    (91, "`book.rated` с `book_id`, `user_id`, `value`, `tenant_id`",
     "`book.rated` с `book_id`, `reader_id`, `score`, `tenant_id`"),
    (91, "`SELECT book_id, AVG(value), COUNT(*) FROM ratings WHERE book_id = ANY($1) GROUP BY book_id`",
     "`SELECT book_id, AVG(score), COUNT(*) FROM signals.ratings WHERE book_id = ANY($1) GROUP BY book_id`"),
    (91, "Не вводить колонку `stars`/`score` — имя `value` из `04-data-model.md`.",
     "Имя колонки `score` из `04-data-model.md` (не `value`, не `stars`)."),
    (91, "`CHECK (value BETWEEN 1 AND 5)`",
     "`CHECK (score BETWEEN 1 AND 5)`"),
    (91, "`04-data-model.md` (таблица `ratings`)",
     "`04-data-model.md` (таблица `signals.ratings`)"),

    # === M5: #92 signal_events DDL ===
    (92, "id          BIGSERIAL PRIMARY KEY,",
     "id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),"),
    (92, "user_id     UUID,",
     "reader_id    UUID,"),
    (92, "payload     JSONB NOT NULL DEFAULT '{}'::jsonb,",
     "metadata     JSONB       NOT NULL DEFAULT '{}',\n      copy_id      UUID,\n      loan_id      UUID,"),
    (92, "occurred_at TIMESTAMPTZ NOT NULL\n  );",
     "occurred_at  TIMESTAMPTZ NOT NULL,\n      ingested_at  TIMESTAMPTZ NOT NULL DEFAULT now()\n  );"),
    (92, "ON signals.signal_events (user_id, occurred_at DESC) WHERE user_id IS NOT NULL;",
     "ON signals.signal_events (tenant_id, reader_id, occurred_at DESC) WHERE reader_id IS NOT NULL;"),
    (92, "user_id        UUID NOT NULL,",
     "reader_id      UUID NOT NULL,"),
    (92, "CONSTRAINT reading_progress_key UNIQUE (tenant_id, user_id, book_id)",
     "CONSTRAINT reading_progress_key UNIQUE (tenant_id, reader_id, book_id)"),
    (92, "`user_id`, `book_id`, `copy_id`, `loan_id`, `tenant_id`",
     "`reader_id`, `book_id`, `copy_id`, `loan_id`, `tenant_id`"),
    (92, "`book_id`, `user_id`, `occurred_at`",
     "`book_id`, `reader_id`, `occurred_at`"),

    # === n1: typo in #97 ===
    (97, "07-non-goals-and-rkills.md", "07-non-goals-and-risks.md"),

    # === #106 Context: add link to #34 ===
    (106, "Продукт: rate limiting на auth-эндпоинтах user-service",
     "Продукт: rate limiting на auth-эндпоинтах user-service (реализация — #34, CLOSED)"),
]


def gh(*args: str) -> str:
    return subprocess.run(
        ["gh", *args], capture_output=True, check=True,
        encoding="utf-8", errors="replace",
    ).stdout


def main(apply: bool) -> None:
    changed: list[int] = []
    for num in range(89, 107):
        body = json.loads(gh("issue", "view", str(num), "--json", "body"))["body"] or ""
        new_body = body
        applied = False
        for issue, old, new in REPLACEMENTS:
            if issue == num and old in new_body:
                new_body = new_body.replace(old, new)
                applied = True
        if applied:
            if apply:
                path = tempfile.mktemp(suffix=".md")
                with open(path, "w", encoding="utf-8") as f:
                    f.write(new_body)
                gh("issue", "edit", str(num), "--body-file", path)
                print(f"+ #{num} edited")
            else:
                print(f"WOULD EDIT #{num}")
            changed.append(num)
        else:
            print(f"= #{num} unchanged")
    print(f"\n{'Applied' if apply else 'Would change'}: {changed}")


if __name__ == "__main__":
    main(apply="--apply" in sys.argv)

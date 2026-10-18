# 04. Data Model

## Принципы

1. **database-per-service:** каждый сервис владеет своей базой. Никаких кросс-базовых FK.
2. **`tenant_id UUID NOT NULL`** во всех новых таблицах с Phase 1. Фильтрация в коде (Phase 1–2), RLS (Phase 3).
3. **UUID** генерирует PostgreSQL (`gen_random_uuid()`).
4. **TIMESTAMPTZ** для всех временных меток.
5. **Частичные уникальные индексы** вместо статусных CHECK где это семантичнее (см. `loans_active_copy_idx` в `services/loan-service/migrations/001_init.sql`).

---

## book-db / schema `signals`

**Владелец:** book-service  
**Миграция:** `services/book-service/migrations/002_signals.sql` (Phase 1)  
**DSN:** тот же `BOOK_SERVICE_DB_DSN`, `search_path` включает `signals`

### Таблица `wishlist`

```sql
-- Phase 1. Wishlist читателя: отложенные книги.
CREATE SCHEMA IF NOT EXISTS signals;

CREATE TABLE signals.wishlist (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID        NOT NULL,
    reader_id   UUID        NOT NULL,
    book_id     UUID        NOT NULL,
    added_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Уведомление об доступности отправлено (идемпотентность).
    notified_at TIMESTAMPTZ,
    CONSTRAINT wishlist_unique_reader_book UNIQUE (tenant_id, reader_id, book_id)
);

-- Hot path: "мой wishlist" с сортировкой по дате добавления.
CREATE INDEX wishlist_reader_idx ON signals.wishlist (tenant_id, reader_id, added_at DESC);

-- Аналитика: какие книги чаще всего в wishlist (acquisition analytics, Phase 3).
CREATE INDEX wishlist_book_idx ON signals.wishlist (tenant_id, book_id);
```

### Таблица `ratings`

```sql
-- Phase 1. Оценка книги читателем (1–5 звёзд).
CREATE TABLE signals.ratings (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID        NOT NULL,
    reader_id   UUID        NOT NULL,
    book_id     UUID        NOT NULL,
    score       SMALLINT    NOT NULL CHECK (score BETWEEN 1 AND 5),
    review      TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ratings_unique_reader_book UNIQUE (tenant_id, reader_id, book_id)
);

-- Средний рейтинг книги (агрегат для карточки).
CREATE INDEX ratings_book_idx ON signals.ratings (tenant_id, book_id);

-- Персонализация: "что этот читатель уже оценил".
CREATE INDEX ratings_reader_idx ON signals.ratings (tenant_id, reader_id, updated_at DESC);
```

### Таблица `signal_events`

```sql
-- Phase 1. Сырые сигналы телеметрии. Append-only таблица.
-- Пишется consumer'ом book-service из RabbitMQ.
CREATE TABLE signals.signal_events (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Идемпотентность: event_id из конверта pkg/events.
    event_id     UUID        NOT NULL,
    tenant_id    UUID        NOT NULL,
    reader_id    UUID        NOT NULL,
    event_type   VARCHAR(64) NOT NULL,
    book_id      UUID,
    -- Произвольные метаданные сигнала (source, position, session_id).
    metadata     JSONB       NOT NULL DEFAULT '{}',
    occurred_at  TIMESTAMPTZ NOT NULL,
    ingested_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Идемпотентность: одно событие = одна запись.
    CONSTRAINT signal_events_event_id_unique UNIQUE (event_id),
    CONSTRAINT signal_events_type_check CHECK (
        event_type IN (
            'book.viewed', 'book.wishlisted', 'book.rated',
            'reading.progress', 'ai.recommend.shown', 'ai.recommend.clicked'
        )
    )
);

-- Временной ряд для аналитики.
CREATE INDEX signal_events_time_idx ON signals.signal_events (tenant_id, occurred_at DESC);

-- "События по книге" (популярность, конверсия).
CREATE INDEX signal_events_book_idx ON signals.signal_events (tenant_id, book_id, event_type, occurred_at DESC);

-- "События читателя" (персонализация, AI-контекст).
CREATE INDEX signal_events_reader_idx ON signals.signal_events (tenant_id, reader_id, occurred_at DESC);
```

### Таблица `reading_progress`

```sql
-- Phase 1a (issue #95). Прогресс чтения (публикует loan-service, consumer — book-service).
CREATE TABLE signals.reading_progress (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id        UUID        NOT NULL,
    tenant_id       UUID        NOT NULL,
    reader_id       UUID        NOT NULL,
    book_id         UUID        NOT NULL,
    loan_id         UUID,
    -- Прогресс: 0–100 (процент) или глава/страница — в metadata.
    percent         SMALLINT    NOT NULL CHECK (percent BETWEEN 0 AND 100),
    occurred_at     TIMESTAMPTZ NOT NULL,
    ingested_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT reading_progress_event_id_unique UNIQUE (event_id),
    -- Одна запись на читателя + книгу (обновляется, а не append).
    CONSTRAINT reading_progress_unique_reader_book UNIQUE (tenant_id, reader_id, book_id)
);

-- Персонализация: "читатель дочитал X до 70%".
CREATE INDEX reading_progress_reader_idx ON signals.reading_progress (tenant_id, reader_id, occurred_at DESC);

-- Аналитика бросания: "какие книги бросают на 30–50%".
CREATE INDEX reading_progress_book_idx ON signals.reading_progress (tenant_id, book_id, percent);
```

---

## clubs-db (managed by user-service)

**Владелец:** user-service  
**Миграция:** `services/user-service/migrations_clubs/001_clubs.sql` (Phase 2)  
**DSN:** `USER_SERVICE_CLUBS_DB_DSN` (новый, отдельный контейнер `clubs-db` в `docker-compose.yml`)

### Таблица `clubs`

```sql
-- Phase 2. Книжный клуб.
CREATE TABLE clubs (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID        NOT NULL,
    title           TEXT        NOT NULL,
    description     TEXT        NOT NULL DEFAULT '',
    -- Куратор — библиотекарь или назначенный читатель.
    curator_id      UUID        NOT NULL,
    -- PENDING (создан читателем, ждёт активации) / ACTIVE / ARCHIVED.
    status          VARCHAR(16) NOT NULL DEFAULT 'PENDING'
                    CHECK (status IN ('PENDING', 'ACTIVE', 'ARCHIVED')),
    -- Книга месяца (ссылка на book-service, FK невозможен).
    current_book_id UUID,
    max_members     INTEGER     NOT NULL DEFAULT 30 CHECK (max_members > 0),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    approved_at     TIMESTAMPTZ,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Список клубов библиотеки.
CREATE INDEX clubs_tenant_status_idx ON clubs (tenant_id, status);

-- Куратор видит свои клубы.
CREATE INDEX clubs_curator_idx ON clubs (curator_id);
```

### Таблица `club_memberships`

```sql
-- Phase 2. Участие читателя в клубе.
CREATE TABLE club_memberships (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID        NOT NULL,
    club_id     UUID        NOT NULL REFERENCES clubs (id) ON DELETE CASCADE,
    user_id     UUID        NOT NULL,
    -- CURATOR (библиотекарь/назначенный) / MEMBER.
    role        VARCHAR(16) NOT NULL DEFAULT 'MEMBER'
                CHECK (role IN ('CURATOR', 'MEMBER')),
    joined_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    left_at     TIMESTAMPTZ,
    CONSTRAINT club_memberships_unique UNIQUE (tenant_id, club_id, user_id)
);

-- "Мои клубы" читателя.
CREATE INDEX club_memberships_user_idx ON club_memberships (tenant_id, user_id)
    WHERE left_at IS NULL;

-- Участники клуба.
CREATE INDEX club_memberships_club_idx ON club_memberships (club_id)
    WHERE left_at IS NULL;
```

### Таблица `club_posts`

```sql
-- Phase 2. Посты в обсуждении клуба.
CREATE TABLE club_posts (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID        NOT NULL,
    club_id         UUID        NOT NULL REFERENCES clubs (id) ON DELETE CASCADE,
    author_id       UUID        NOT NULL,
    body            TEXT        NOT NULL,
    -- Скрыт модератором (soft-delete, виден автору).
    hidden          BOOLEAN     NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Лента клуба ( newest first ).
CREATE INDEX club_posts_club_idx ON club_posts (club_id, created_at DESC);

-- Посты автора.
CREATE INDEX club_posts_author_idx ON club_posts (tenant_id, author_id, created_at DESC);
```

### Таблица `club_questions`

```sql
-- Phase 2. AI-вопросы для обсуждения.
CREATE TABLE club_questions (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID        NOT NULL,
    club_id         UUID        NOT NULL REFERENCES clubs (id) ON DELETE CASCADE,
    creator_id      UUID        NOT NULL,
    book_id         UUID        NOT NULL,
    question        TEXT        NOT NULL,
    -- AI-сгенерирован или отредактирован вручную.
    ai_generated    BOOLEAN     NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Вопросы клуба.
CREATE INDEX club_questions_club_idx ON club_questions (club_id, created_at DESC);
```

### Таблица `club_moderation`

```sql
-- Phase 2. Журнал модерации (жалобы, скрытия, восстановления).
CREATE TABLE club_moderation (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID        NOT NULL,
    club_id         UUID        NOT NULL REFERENCES clubs (id) ON DELETE CASCADE,
    target_type     VARCHAR(16) NOT NULL CHECK (target_type IN ('POST', 'MEMBERSHIP')),
    target_id       UUID        NOT NULL,
    reporter_id     UUID,
    moderator_id    UUID,
    -- REPORTED / HIDDEN / RESTORED / DISMISSED.
    action          VARCHAR(16) NOT NULL
                    CHECK (action IN ('REPORTED', 'HIDDEN', 'RESTORED', 'DISMISSED')),
    reason          TEXT        NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Очередь модерации библиотекаря.
CREATE INDEX club_moderation_tenant_pending_idx ON club_moderation (tenant_id, created_at DESC)
    WHERE moderator_id IS NULL;
```

---

## billing-db (Phase 3, managed by user-service)

**Владелец:** user-service  
**Миграция:** `services/user-service/migrations_billing/001_billing.sql` (Phase 3)  
**DSN:** `USER_SERVICE_BILLING_DB_DSN` (новый контейнер `billing-db`)

### Таблица `subscriptions`

```sql
-- Phase 3. SaaS-подписка библиотеки.
CREATE TABLE subscriptions (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID        NOT NULL,
    -- TRIAL / ACTIVE / PAST_DUE / CANCELLED / EXPIRED.
    status              VARCHAR(16) NOT NULL DEFAULT 'TRIAL'
                        CHECK (status IN ('TRIAL', 'ACTIVE', 'PAST_DUE', 'CANCELLED', 'EXPIRED')),
    -- Название тарифа (free / standard / pro).
    plan                VARCHAR(32) NOT NULL DEFAULT 'standard',
    -- Максимум читателей на тарифе.
    max_readers         INTEGER     NOT NULL DEFAULT 1000,
    -- Внешний ID в платёжном провайдере (ЮKassa/Tinkoff).
    external_id         VARCHAR(128),
    payment_provider    VARCHAR(16) CHECK (payment_provider IN ('YUKASSA', 'TINKOFF')),
    current_period_start TIMESTAMPTZ NOT NULL,
    current_period_end   TIMESTAMPTZ NOT NULL,
    cancel_at_period_end  BOOLEAN    NOT NULL DEFAULT false,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX subscriptions_tenant_idx ON subscriptions (tenant_id, status);
```

### Таблица `payments`

```sql
-- Phase 3. История платежей. PAN/CVV никогда не хранятся.
CREATE TABLE payments (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID        NOT NULL,
    subscription_id     UUID        NOT NULL REFERENCES subscriptions (id),
    -- Идемпотентный ключ из webhook платёжного провайдера.
    idempotency_key     VARCHAR(128) NOT NULL,
    payment_provider    VARCHAR(16) NOT NULL CHECK (payment_provider IN ('YUKASSA', 'TINKOFF')),
    -- Внешний ID платежа в провайдере.
    external_payment_id VARCHAR(128),
    amount_kopecks      BIGINT      NOT NULL CHECK (amount_kopecks > 0),
    currency            VARCHAR(3)  NOT NULL DEFAULT 'RUB',
    -- PENDING / SUCCEEDED / FAILED / REFUNDED.
    status              VARCHAR(16) NOT NULL DEFAULT 'PENDING'
                        CHECK (status IN ('PENDING', 'SUCCEEDED', 'FAILED', 'REFUNDED')),
    -- Ответ webhook (для аудита).
    provider_response   JSONB       NOT NULL DEFAULT '{}',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at        TIMESTAMPTZ,
    CONSTRAINT payments_idempotency_unique UNIQUE (idempotency_key)
);

CREATE INDEX payments_subscription_idx ON payments (subscription_id, created_at DESC);
```

---

## loan-db: holds и fines (Phase 3)

**Владелец:** loan-service  
**Миграция:** `services/loan-service/migrations/002_holds.sql`, `003_fines.sql` (Phase 3)

### Таблица `holds`

```sql
-- Phase 3. Очередь ожидания на книгу.
CREATE TABLE holds (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID        NOT NULL,
    reader_id   UUID        NOT NULL,
    book_id     UUID        NOT NULL,
    -- ACTIVE / READY (копия доступна, ждёт выдачи) / FULFILLED / CANCELLED / EXPIRED.
    status      VARCHAR(16) NOT NULL DEFAULT 'ACTIVE'
                CHECK (status IN ('ACTIVE', 'READY', 'FULFILLED', 'CANCELLED', 'EXPIRED')),
    -- Позиция в очереди (вычисляется при создании).
    position    INTEGER     NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    ready_at    TIMESTAMPTZ,
    fulfilled_at TIMESTAMPTZ,
    cancelled_at TIMESTAMPTZ,
    -- Одна активная очередь на читателя + книгу.
    CONSTRAINT holds_unique_active UNIQUE (tenant_id, reader_id, book_id)
        WHERE status IN ('ACTIVE', 'READY')
);

-- Очередь на книгу (FIFO).
CREATE INDEX holds_book_queue_idx ON holds (tenant_id, book_id, position)
    WHERE status IN ('ACTIVE', 'READY');

-- "Мои очереди" читателя.
CREATE INDEX holds_reader_idx ON holds (tenant_id, reader_id, created_at DESC)
    WHERE status IN ('ACTIVE', 'READY');
```

### Таблица `fines`

```sql
-- Phase 3. Штрафы за просрочку.
CREATE TABLE fines (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID        NOT NULL,
    reader_id   UUID        NOT NULL,
    loan_id     UUID        NOT NULL,
    book_id     UUID        NOT NULL,
    amount_kopecks BIGINT   NOT NULL CHECK (amount_kopecks >= 0),
    -- ACCRUED / PAID / WAIVED.
    status      VARCHAR(16) NOT NULL DEFAULT 'ACCRUED'
                CHECK (status IN ('ACCRUED', 'PAID', 'WAIVED')),
    reason      TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    paid_at     TIMESTAMPTZ,
    waived_at   TIMESTAMPTZ
);

-- "Мои штрафы" читателя.
CREATE INDEX fines_reader_idx ON fines (tenant_id, reader_id, created_at DESC);

-- Неоплаченные штрафы (для блокировки выдачи).
CREATE INDEX fines_unpaid_idx ON fines (tenant_id, status)
    WHERE status = 'ACCRUED';
```

---

## Phase 3: миграция существующих таблиц (tenant_id + RLS)

**Эпик:** «Multi-tenant для существующих сервисов»  
**Фаза:** Phase 3  
**Статус:** TBD, детализация в Phase 3

### Что меняется

| Сервис | Таблицы | Миграция |
| --- | --- | --- |
| book-service | `books`, `book_copies` | `services/book-service/migrations/004_tenant.sql` |
| user-service | `users`, `sessions` | `services/user-service/migrations/003_tenant.sql` |
| loan-service | `loans` | `services/loan-service/migrations/004_tenant.sql` |
| ai-service (pgvector) | `books`, `book_chunks` | `services/ai-service/rag/db/02_tenant.sql` |

### Шаблон миграции

```sql
-- Phase 3, эпик multi-tenant. Не применять до готовности RLS-политик.
ALTER TABLE books ADD COLUMN tenant_id UUID;
-- Наполнение: UPDATE books SET tenant_id = '<default_tenant>';
-- ALTER TABLE books ALTER COLUMN tenant_id SET NOT NULL;

-- RLS: включить на таблице.
ALTER TABLE books ENABLE ROW LEVEL SECURITY;

-- Политика: приложение с tenant_id в сессии видит только свои строки.
CREATE POLICY tenant_isolation ON books
    USING (tenant_id = current_setting('app.tenant_id')::uuid);
```

### Почему отдельный эпик

- Миграция данных (UPDATE на миллионах строк) требует downtime или pg_repack.
- RLS-политики нужно тестировать на каждом запросе (все репозитории всех сервисов).
- Приложения должны передавать `SET LOCAL app.tenant_id = $1` в каждой транзакции.

---

## Сводная таблица: таблицы × фазы

| Таблица | Сервис | Схема | Фаза | tenant_id |
| --- | --- | --- | --- | --- |
| `wishlist` | book-service | `signals` | Phase 1 | ✅ |
| `ratings` | book-service | `signals` | Phase 1 | ✅ |
| `signal_events` | book-service | `signals` | Phase 1 | ✅ |
| `reading_progress` | book-service | `signals` | Phase 1a (issue #95) | ✅ |
| `clubs` | user-service | `clubs-db` | Phase 2 | ✅ |
| `club_memberships` | user-service | `clubs-db` | Phase 2 | ✅ |
| `club_posts` | user-service | `clubs-db` | Phase 2 | ✅ |
| `club_questions` | user-service | `clubs-db` | Phase 2 | ✅ |
| `club_moderation` | user-service | `clubs-db` | Phase 2 | ✅ |
| `subscriptions` | user-service | `billing-db` | Phase 3 | ✅ |
| `payments` | user-service | `billing-db` | Phase 3 | ✅ |
| `holds` | loan-service | `loan-db` | Phase 3 | ✅ |
| `fines` | loan-service | `loan-db` | Phase 3 | ✅ |
| `books`, `book_copies` | book-service | `public` | Phase 3 (миграция) | ✅ (миграция) |
| `users`, `sessions` | user-service | `public` | Phase 3 (миграция) | ✅ (миграция) |
| `loans` | loan-service | `public` | Phase 3 (миграция) | ✅ (миграция) |

---

## Ссылки

- Существующие схемы: `services/book-service/migrations/001_init.sql`, `services/user-service/migrations/001_init.sql`, `services/loan-service/migrations/001_init.sql`
- Архитектура: `docs/product/03-architecture-deltas.md`
- API: `docs/product/05-api-additions.md`
- Сигналы: `docs/product/06-signals-and-analytics.md`

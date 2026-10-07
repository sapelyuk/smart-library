# KODA.md — контекст проекта

## Обзор проекта

**Smart Library** — полнофункциональная платформа управления библиотекой: монорепо с бэкендом на Go (gRPC-микросервисы), планируемым React-приложением и AI-рекомендациями книг на LLM/RAG. Реализовано: **Book Service полностью рабочий** (gRPC-API, REST и Swagger через grpc-gateway, домен, бизнес-логика, PostgreSQL-хранилище, тесты), **User Service рабочий** (gRPC-API, REST и Swagger, регистрация/вход, хеширование паролей, сессии, ролевая модель, PostgreSQL-хранилище, миграции, unit-тесты домена и security), **API Gateway рабочий** (единая HTTP-точка входа, маршрутизация к сервисам, gRPC-аутентификация через user-service), **RAG-прототип AI-модуля работает** (n8n + pgvector + Gemini, 20 книг проиндексировано), плюс общий модуль `pkg` (logger, config, migrate). Остальные сервисы (Loan, Notification, Go-адаптер AI) существуют только как строки в архитектурной таблице и заведённые задачи.

- **Назначение:** каталог книг, учёт читателей и библиотекарей, выдача/возврат книг, уведомления о сроках возврата, единая точка входа для клиентов.
- **Язык и стек:** Go (локально установлен `go1.27.1 windows/amd64`), gRPC, protobuf, `log/slog`, PostgreSQL (`pgx/v5` в book-service, `database/sql` + `lib/pq` в user-service); брокер выбран — RabbitMQ (ADR-0001), discovery — Kubernetes DNS (ADR-0003); фронтенд — React (в плане); AI-модуль — LLM/RAG (прототип работает, Go-адаптер в плане).
- **Архитектурный стиль:** микросервисы с изолированным хранилищем на каждый сервис (паттерн *database-per-service*).
- **Организация кода:** монорепо с Go-воркспейсом (`go.work`) для локальной разработки нескольких модулей одновременно.

## Топология сервисов

| Сервис | Зона ответственности | Порт |
| --- | --- | --- |
| API Gateway | Единая точка входа, маршрутизация запросов | 8080 |
| Book Service | Каталог книг, ISBN, экземпляры | 8081 |
| User Service | Читатели, библиотекари, аутентификация | 8082 |
| Loan Service | Выдача/возврат, сроки возврата | 8083 |
| Notification Service | Email/SMS-уведомления о сроках | 8084 |
| AI Service | Рекомендации книг через LLM/RAG (ADR-0002) | 8085 |

## Паттерны взаимодействия

- **Синхронная коммуникация:** gRPC между сервисами (быстро, типизированно).
- **Асинхронная коммуникация:** RabbitMQ (topic exchange `library.events`, отложенная доставка через TTL+DLX для напоминаний о сроках возврата).
- **Обнаружение сервисов:** Kubernetes DNS (продакшен) / Docker Compose DNS (локально), ADR-0003.
- **Хранилище:** отдельная база PostgreSQL на каждый сервис.

## План развития

| Этап | Что реализовано | Статус |
| --- | --- | --- |
| Бэкенд-сервисы | Book Service, User Service, API Gateway, RabbitMQ | ✅ готово |
| Фронтенд | React SPA, потребляющая REST-контракты сервисов | 🔜 в плане |
| AI-модуль | AI Service: ADR-0002 + RAG-прототип (n8n + pgvector + Gemini) | 🟡 прототип работает (#22), Go-адаптер — #23 |
| Discovery | Kubernetes DNS (Docker Compose DNS локально) | 📋 ADR-0003 принято |
| CI/CD | GitHub Actions (CI: build + test), контейнеризация | 🟡 CI готов (PR #28), контейнеризация готова (#6); CD — в плане |

**Приоритет:** фронтенд → AI-модуль → discovery → CI/CD.

## Структура каталога (фактическое состояние)

```
smart-library/
├── go.work                      # воркспейс: ./pkg, ./services/api-gateway, ./services/book-service, ./services/user-service, ./services/loan-service
├── README.md                    # архитектурная спецификация проекта
├── KODA.md                      # этот файл
├── docker-compose.yml           # сервисы + БД + RabbitMQ: api-gateway (:8080), book/user/loan-service, book-db (:5434), user-db (:5432), loan-db (:5435), pgvector (:5433)
├── .dockerignore                # исключает .git, go.work, артефакты из контекста сборки
├── .github/workflows/ci.yml     # CI: build + test (четыре модуля, coverage в step summary)
├── docs/adr/
│   ├── 0001-message-broker.md   # ADR: выбор RabbitMQ, модель событий, гарантии доставки
│   └── 0002-ai-recommendation-architecture.md  # ADR: AI Service поверх n8n RAG, вариант C как развитие
├── scripts/
│   └── gen_proto.ps1            # перегенерация кода (protoc + 4 плагина)
├── third_party/                 # внешние .proto: google/api, protoc-gen-openapiv2/options
├── tools/
│   └── protoc/                  # локальный protoc 36.2 (bin/protoc.exe)
├── pkg/                         # модуль github.com/sapelyuk/smart-library/pkg
│   ├── config/config.go         # String/Int/Duration/RequireString из env
│   ├── logger/logger.go         # slog: json|text, debug..error, MustNew
│   └── migrate/migrate.go       # SQL-миграции: таблица schema_migrations, checksum, Apply
└── services/
    ├── api-gateway/             # модуль github.com/sapelyuk/smart-library/services/api-gateway
    │   ├── cmd/server/main.go   # GATEWAY_* env, gRPC-аутентификация через user-service, HTTP :8080
    │   ├── internal/auth/       # Verifier (gRPC AuthenticateToken), Authenticator, RequireRole + тесты
    │   ├── internal/proxy/      # httputil.ReverseProxy: маршрутизация к book/user-service
    │   └── Dockerfile           # multi-stage образ, контекст сборки — корень репозитория
    ├── book-service/            # модуль github.com/sapelyuk/smart-library/services/book-service
    │   ├── README.md            # документация сервиса: API, env, grpcurl/curl-примеры
    │   ├── Dockerfile           # multi-stage образ, контекст сборки — корень репозитория
    │   ├── cmd/server/main.go   # конфиг, обязательный DSN, миграции, gRPC :8081 + HTTP :8091
    │   ├── proto/book/v1/book.proto  # контракт BookService (9 RPC) + аннотации google.api.http
    │   ├── gen/go/book/v1/      # book.pb.go, book_grpc.pb.go, book.pb.gw.go — генерация, не править
    │   ├── docs/                # book/v1/book.swagger.json (генерация) + docs.go (embed)
    │   ├── internal/
    │   │   ├── domain/          # Book, Copy, CopyStatus, ISBN, ошибки + тесты
    │   │   ├── service/         # BookService (use-case'ы) + тесты против PostgreSQL
    │   │   ├── repository/      # контракты + postgres/ (pgx/v5, единственная реализация)
    │   │   ├── testdb/          # провижининг тестовой БД: миграции, advisory-lock, truncate
    │   │   └── handler/         # grpc.go: прото <-> домен; http.go: gateway + Swagger UI
    │   └── migrations/          # 001_init.sql + migrations.go (embed FS), применяется при старте
    ├── user-service/            # модуль github.com/sapelyuk/smart-library/services/user-service
    │   ├── README.md            # документация сервиса: API, env, RBAC, provisioning БД
    │   ├── Dockerfile           # multi-stage образ, контекст сборки — корень репозитория
    │   ├── cmd/server/main.go   # конфиг, миграции, сид библиотекаря, purge, gRPC :8082 + HTTP :8092
    │   ├── proto/user/v1/user.proto  # контракт UserService (12 RPC) + аннотации google.api.http
    │   ├── gen/go/user/v1/      # user.pb.go, user_grpc.pb.go, user.pb.gw.go — генерация, не править
    │   ├── docs/                # user/v1/user.swagger.json (генерация) + docs.go (embed)
    │   ├── internal/
    │   │   ├── domain/          # User, Role, UserStatus, Email, PasswordPolicy, Session, Principal (RBAC)
    │   │   ├── security/        # argon2id (PHC), токены (base64url + SHA-256)
    │   │   ├── service/         # use-case'ы: регистрация, вход, RBAC, смена пароля
    │   │   ├── repository/      # контракты + postgres/ (database/sql, lib/pq)
    │   │   └── handler/         # gRPC-адаптер, auth-интерцептор, gateway + Swagger UI
    │   └── migrations/          # 001_init.sql (users, sessions) + migrations.go (embed FS)
    ├── loan-service/            # модуль github.com/sapelyuk/smart-library/services/loan-service
    │   ├── README.md            # документация сервиса: API, env, RBAC, события, примеры
    │   ├── Dockerfile           # multi-stage образ, копирует pkg + book/user-service (replace)
    │   ├── cmd/server/main.go   # конфиг LOAN_SERVICE_*, миграции, gRPC :8083 + HTTP :8093
    │   ├── proto/loan/v1/loan.proto  # контракт LoanService (6 RPC) + аннотации google.api.http
    │   ├── gen/go/loan/v1/      # loan.pb.go, loan_grpc.pb.go, loan.pb.gw.go — генерация, не править
    │   ├── docs/                # loan/v1/loan.swagger.json (генерация) + docs.go (embed)
    │   ├── internal/
    │   │   ├── domain/          # Loan, LoanStatus (OVERDUE выводится), Principal (RBAC), ошибки + тесты
    │   │   ├── service/         # use-case'ы выдачи/возврата/продления + тесты с моками
    │   │   ├── repository/      # контракт + postgres/ (pgx/v5) + тесты против PostgreSQL
    │   │   ├── bookclient/      # gRPC-клиент book-service (BorrowCopy/ReturnCopy)
    │   │   ├── usersvc/         # gRPC-клиент user-service (AuthenticateToken)
    │   │   ├── auth/            # интерцептор: bearer-токен -> Principal + тесты
    │   │   ├── events/          # порт событий (LogPublisher до pkg/events #14)
    │   │   ├── testdb/          # провижининг тестовой БД: миграции, advisory-lock, truncate
    │   │   └── handler/         # grpc.go: прото <-> домен; http.go: gateway + Swagger UI + тесты
    │   └── migrations/          # 001_init.sql (loans, частичный unique на copy_id) + migrations.go
    └── ai-service/              # ADR-0002; RAG-прототип работает, см. services/ai-service/README.md
        ├── README.md            # статус, плановый контракт ai.v1.AiService
        └── rag/                 # рабочий прототип RAG (n8n workflow, pgvector схема, скрипты, eval-набор)
            ├── README.md        # назначение, быстрый старт, env-переменные
            ├── docker-compose.yml  # отдельный pgvector-стек для работы с прототипом
            ├── db/              # 01-schema.sql (расширение, таблицы, функции), smoke-test, assertions
            ├── workflow/        # экспорт n8n workflow (book-rag-system.json)
            ├── scripts/         # start/verify/import/ingest/eval скрипты (PowerShell + python)
            │   └── eval-rag.ps1  # прогон eval-набора: retrieve|agent, метрики, отчёт в evals/reports/
            ├── evals/           # **eval-набор** (контракт того, что считать хорошим ответом)
            │   ├── rag-eval-suite.json  # 21 вопрос: taste/self/author/genre/constraint/refuse, ожидаемые id
            │   └── reports/     # отчёты прогонов (JSON + Markdown), .gitignore кроме README
            ├── tests/           # pytest-набор метрик eval-набора (hit-rate, MRR, precision@k, refuse-скор)
            ├── pytest.ini       # python -m pytest tests
            ├── samples/books.csv  # 20 книг для индексации
            └── docs/            # документация прототипа (ARCHITECTURE/SETUP/USAGE)
```

Go-модуль `services/notification-service` на диске **отсутствует** — заведена задача #48 (Notification). Go-модуль `services/ai-service` ещё не создан (задача #23), но в `rag/` лежит **рабочий** RAG-прототип (без Go-кода): pgvector, схема, n8n workflow, проиндексировано 20 книг, плюс **eval-набор** (`evals/` + `scripts/eval-rag.ps1` + `tests/`) — офлайн-валидация: `powershell -File scripts/eval-rag.ps1 -ValidateOnly`.

## Статус реализации

| Компонент | Состояние |
| --- | --- |
| `pkg/logger`, `pkg/config`, `pkg/migrate` | готово, используется обоими сервисами |
| Book Service: контракт, домен, сервис, handler, запуск | готов, собирается и работает |
| REST-слой и Swagger Book Service | готов: grpc-gateway на `:8091`, Swagger UI на `/swagger/` |
| Хранилище Book Service | **PostgreSQL 17** (`pgx/v5/stdlib`), единственная реализация; `BOOK_SERVICE_DB_DSN` обязателен |
| PostgreSQL-реализация репозитория Book Service | **готово** (issue #9, PG 17 + `pgx/v5`): `AcquireAvailable` через `FOR UPDATE SKIP LOCKED` + `FOR SHARE`, миграции через `pkg/migrate` при старте |
| Тесты Book Service | домен, сервис и postgres-репозиторий — против PostgreSQL (`POSTGRES_TEST_DSN`, иначе skip), HTTP-слой — `httptest`; in-memory пакет удалён |
| User Service: контракт, домен, security, сервис, handler, запуск | готов, собирается и работает |
| REST-слой и Swagger User Service | готов: grpc-gateway на `:8092`, Swagger UI на `/swagger/` |
| Аутентификация и RBAC | готово: argon2id-пароли, bearer-токены (в БД только SHA-256-хеш), сессии с TTL, роли READER/LIBRARIAN, интерцептор + проверки в домене |
| Хранилище User Service | PostgreSQL 17 в Docker (`database/sql` + `lib/pq`), миграции при старте |
| Тесты User Service | домен, security (PR #11) и HTTP-слой (PR #26) — готово; сервис и репозиторий — нет |
| API Gateway | **готово** (issue #10): reverse-proxy с gRPC-аутентификацией через user-service, маршрутизация по пути, `GATEWAY_*` env vars |
| Loan Service | **готово** (issue #49): gRPC `:8083` + REST/Swagger `:8093`, выдача/возврат/продление, PostgreSQL 17 (`pgx/v5`), gRPC-клиенты к book- и user-service, auth-интерцептор, события `loan.*` через `LogPublisher` (до `pkg/events` #14) |
| Тесты Loan Service | домен, auth-интерцептор, сервис (с моками), PostgreSQL-репозиторий (против БД, `POSTGRES_TEST_DSN`, иначе skip), HTTP-слой (`httptest`) |
| Notification Service | не начато; заведена задача #48 (напоминания о сроках, консьюмер `loan.*`) |
| gRPC-клиенты между сервисами, события, discovery | loan-service вызывает book-service (`BorrowCopy`/`ReturnCopy`) и user-service (`AuthenticateToken`); discovery: ADR-0003 (Kubernetes DNS); брокер: ADR-0001 (RabbitMQ); реализация событий — #14 |
| CI | GitHub Actions: build + test с кэшем модулей и coverage в step summary (PR #28) |
| Контейнеризация | готово: multi-stage Dockerfile для book-, user- и loan-service, сервисы и БД в `docker-compose.yml`, healthcheck на `GET /healthz` (issue #6) |
| Событийная шина: выбор брокера | ADR-0001 (RabbitMQ), локальный RabbitMQ в `docker-compose.yml`; реализация — issue #14 |
| AI Service: RAG-прототип | **работает end-to-end**: n8n + pgvector + Gemini, 20 книг проиндексировано, chat UI и webhook прошли smoke-тест; артефакты в `services/ai-service/rag/` (#22). Оценка качества: `evals/rag-eval-suite.json` (21 вопрос: taste/self/author/genre/constraint/refuse) + `scripts/eval-rag.ps1` + `scripts/eval_metrics.py` + `tests/` (53 offline-теста, pytest), метрики precision@k/recall@k/hit@k/MRR, офлайн-валидация набора без сети и API-ключей (`-ValidateOnly`) |
| AI Service: Go-адаптер `ai.v1.AiService` | не начато (issue #23); архитектура — ADR-0002; pgvector в `docker-compose.yml` (`ai-rag-db`, `:5433`) |

## Ключевые файлы

- `README.md` — архитектурная спецификация: таблица сервисов с портами, паттерны коммуникации, целевая структура. Источник истины по архитектурным решениям.
- `book-service/README.md` — документация сервиса: методы API, env-переменные, примеры `grpcurl`, команда генерации протобуфа.
- `book-service/proto/book/v1/book.proto` — контракт `book.v1.BookService`: `CreateBook`, `GetBook`, `ListBooks`, `UpdateBook`, `DeleteBook`, `AddBookCopy`, `ListBookCopies`, `BorrowBookCopy`, `ReturnBookCopy`. Каждый RPC аннотирован `google.api.http` — это источник и REST-маршрутов, и Swagger-спецификации. После правки — перегенерировать код.
- `book-service/internal/handler/http.go` — HTTP-слой: grpc-gateway монтируется на `/v1/`, Swagger UI на `/swagger/`, спецификация на `/swagger/swagger.json`. Вызывает gRPC-сервер этого же процесса через локальный клиент.
- `book-service/docs/docs.go` — `//go:embed` сгенерированной `book/v1/book.swagger.json`; сам JSON правится только перегенерацией.
- `book-service/internal/repository/postgres/store.go` — PostgreSQL-реализация обоих контрактов (`pgx/v5/stdlib`). `AcquireAvailable` — транзакция с `FOR SHARE` на книге и `SELECT ... FOR UPDATE SKIP LOCKED` на экземпляре; ошибки PostgreSQL переводятся в доменные (`unique ISBN/barcode`, FK RESTRICT → `ErrBookHasActiveLoans`, no rows → `ErrNotFound`). `Delete` удаляет копии перед книгой в одной транзакции (совместимо с `ON DELETE RESTRICT`).
- `book-service/internal/testdb/testdb.go` — провижининг тестовой БД: читает `POSTGRES_TEST_DSN` (иначе skip), держит advisory-lock на процесс, применяет миграции, очищает таблицы.
- `book-service/migrations/001_init.sql` — схема: `citext` для ISBN, ICU-коллация `"ru_RU_icu"`, `ON DELETE RESTRICT`, GIN-индексы `to_tsvector('russian', ...)`; `gen_random_uuid()` без `pgcrypto`.
- `book-service/internal/domain/errors.go` — доменные ошибки; handler переводит их в коды gRPC, наружу `Internal` не течёт.
- `scripts/gen_proto.ps1` — ждёт `protoc` в `tools/protoc/bin` и `$GOPATH/bin` в `PATH`; подключает `-I third_party` и плагины `protoc-gen-grpc-gateway`, `protoc-gen-openapiv2`.
- `third_party/` — `.proto` зависимости (`google/api/annotations.proto`, `google/api/http.proto`, `protoc-gen-openapiv2/options/*`): только include-путь, код из них не генерируется.
- `user-service/README.md` — документация сервиса: методы API, env-переменные, RBAC, Docker-база.
- `user-service/proto/user/v1/user.proto` — контракт `user.v1.UserService`: `Register`, `Login`, `Logout`, `GetCurrentUser`, `AuthenticateToken` (внутренний), `CreateUser`, `GetUser`, `ListUsers`, `UpdateUser`, `ChangePassword`, `DeactivateUser`, `RestoreUser`.
- `user-service/internal/domain/principal.go` — RBAC: `RequireLibrarian`, `AccountAccess`, `UpdateAccess`, `PasswordChange`, `StatusChange` (включая запрет самоблокировки библиотекаря).
- `user-service/internal/security/` — `password.go` (argon2id в PHC-формате, `DummyPasswordHash` для выравнивания времени входа) и `token.go` (случайный токен, в БД — SHA-256-хеш).
- `user-service/internal/handler/interceptor.go` — извлечение Bearer-токена из metadata, публичные методы (`Register`, `Login`), кладо principal в контекст.
- `user-service/migrations/` — `001_init.sql` в goose-формате + `migrations.go` с `//go:embed`; применяется `pkg/migrate` при старте.
- `loan-service/README.md` — документация сервиса: методы API, env-переменные, RBAC, события, примеры.
- `loan-service/proto/loan/v1/loan.proto` — контракт `loan.v1.LoanService`: `Borrow`, `Return`, `Renew`, `Get`, `List`, `ListOverdue`. `LoanStatus` хранит только ACTIVE/RETURNED, OVERDUE выводится из `due_at`. Каждый RPC аннотирован `google.api.http` — источник REST-маршрутов и Swagger; после правки — перегенерировать код.
- `loan-service/internal/domain/loan.go` — агрегат `Loan`, `LoanStatus`, `EffectiveStatus` (OVERDUE на лету), `NewLoan`/`Return`/`Renew`, границы срока; `principal.go` — RBAC (`RequireBorrowAccess`/`RequireLoanAccess`/`RequireLibrarian`).
- `loan-service/internal/service/loan_service.go` — use-case'ы; резервирует экземпляр в book-service до записи выдачи и освобождает его при провале записи/возврате; читатель в `List`/`ListOverdue` жёстко ограничен своими выдачами.
- `loan-service/internal/repository/postgres/store.go` — PostgreSQL-реализация контракта (`pgx/v5/stdlib`); unique-violation активной копии → `ErrCopyAlreadyOnLoan`, no rows → `ErrNotFound`.
- `loan-service/migrations/001_init.sql` — схема `loans`; частичный уникальный индекс `loans_active_copy_idx ON loans (copy_id) WHERE returned_at IS NULL` (экземпляр не выдаётся дважды, история сохраняется), `CHECK (due_at > borrowed_at)`, UUID без внешних ключей (database-per-service).
- `loan-service/internal/bookclient/bookclient.go` — gRPC-клиент book-service (`BorrowCopy`/`ReturnCopy`, статус → доменная ошибка); `internal/usersvc/client.go` — `AuthenticateToken` user-service; `internal/auth/auth.go` — интерцептор bearer-токена → `Principal`, health-методы публичны.
- `docs/adr/0001-message-broker.md` — решение по брокеру (RabbitMQ), сравнение с Kafka/NATS по критериям issue #7, модель событий: topic exchange `library.events`, routing key = `<aggregate>.<action>`, конверт `{event_id, event_type, occurred_at, payload}`, publisher confirms + ручной ack, отложенная доставка через `x-message-ttl` + `x-dead-letter-exchange`. Реализация — issue #14.
- `docs/adr/0002-ai-recommendation-architecture.md` — решение по AI-модулю (принято): `ai-service` — тонкий Go-адаптер с контрактом `ai.v1.AiService` (Recommend, IngestBook), n8n RAG остаётся внутренней реализацией, каталог синхронизируется событиями `book.*` (источник истины — Book Service). Вариант C (нативный Go-порт RAG) — задокументированное направление развития. Задачи: #22 (перенос артефактов, готово), #23 (реализация Go-адаптера).
- `docs/adr/0003-service-discovery.md` — решение по обнаружению сервисов (принято): Kubernetes DNS для продакшена, Docker Compose DNS для локальной разработки. Consul отвергнут из-за избыточной операционной нагрузки.
- `services/ai-service/rag/` — **работающий** прототип RAG: `db/01-schema.sql` (pgvector: `books`, `book_chunks` как `halfvec(3072)`, HNSW-индекс, функции `match_book_chunks`/`get_book`/`list_books`/`delete_book`), `workflow/book-rag-system.json` (n8n, agentic RAG с 4 инструментами), `scripts/` (start/verify/ingest/eval), `samples/books.csv` (20 книг), `docs/` (ARCHITECTURE/SETUP/USAGE), `evals/` (30-вопросовый набор: taste/self/author/genre/constraint/refuse), `tests/` (pytest: precision@k/recall@k/hit@k/refusal-accuracy). Эмбеддинги — Gemini `models/gemini-embedding-001` (3072 dims), chat-модель изолирована в одном узле. Секреты — только в `.env` (git-ignored), шаблон `.env.example`.
- `docker-compose.yml` — сервисы + инфраструктура. RabbitMQ (`rabbitmq:4-management`): AMQP `:5672`, management UI `:15672`, healthcheck `rabbitmq-diagnostics -q ping`, volume `rabbitmq-data`; логин/пароль — из `RABBITMQ_USER`/`RABBITMQ_PASS` (по умолчанию `guest`). PostgreSQL для user-service (`postgres:17`, контейнер `library-user-db`, хост-порт `:5432`, volume `user-db-data`): миграции применяются через `pkg/migrate` при старте. PostgreSQL для book-service (`postgres:17`, контейнер `library-book-db`, хост-порт `:5434`, volume `book-db-data`): миграции применяются через `pkg/migrate` при старте, сервис подключается к нему по `BOOK_SERVICE_DB_DSN` и ждёт `condition: service_healthy`. PostgreSQL для loan-service (`postgres:17`, контейнер `library-loan-db`, хост-порт `:5435`, volume `loan-db-data`): миграции применяются через `pkg/migrate` при старте. pgvector (`pgvector/pgvector:pg17`, контейнер `library-ai-rag-db`, хост-порт `:5433`, volume `ai-rag-db-data`): init-скрипты монтируются из `services/ai-service/rag/db/`, учётные данные — из `AI_RAG_DB_USER`/`AI_RAG_DB_PASSWORD`/`AI_RAG_DB_NAME` (по умолчанию `bookrag`). Сервисные контейнеры `library-book-service`/`library-user-service`/`library-loan-service` собираются из Dockerfile'ов (контекст — корень репо), публикуют gRPC/REST-порты и проходят healthcheck на `GET /healthz`; `user-service` ждёт `user-db`, `loan-service` ждёт `loan-db` по `condition: service_healthy`.
- `services/*/Dockerfile` — multi-stage: `golang:1.27-alpine` (build, `GOWORK=off`, `CGO_ENABLED=0`) → `alpine:3.21` (runtime, non-root uid 10001/10002/10003, `wget` для healthcheck). Контекст сборки — **корень репозитория**, т.к. `go.mod` сервисов содержит `replace ... => ../../pkg`; `.dockerignore` исключает `.git`, `go.work`, артефакты. Точка входа — `./cmd/server`. `loan-service/Dockerfile` дополнительно копирует исходники `book-service` и `user-service` (модуль заменяет их, импортируя сгенерированные proto-пакеты).

## Сборка и запуск

Воркспейс уже инициализирован (`go.work`: `./pkg`, `./services/api-gateway`, `./services/book-service`, `./services/user-service`, `./services/loan-service`). Команды выполняются из директории соответствующего модуля (из корня `go build ./...` не работает — корень не является модулем):

| Задача | Команда | Где выполнять |
| --- | --- | --- |
| Сборка | `go build ./...` | `book-service/`, `user-service/`, `loan-service/`, `pkg/` |
| Статический анализ | `go vet ./...` | `book-service/`, `user-service/`, `loan-service/`, `pkg/` |
| Форматирование | `gofmt -l .` (список), `gofmt -w .` (править) | `book-service/`, `user-service/`, `loan-service/`, `pkg/` |
| Тесты | `go test ./...` | `book-service/`, `user-service/`, `loan-service/`, `pkg/` |
| Запуск Book Service | `go run ./cmd/server` (gRPC на `:8081`, REST + Swagger на `:8091`), нужен `BOOK_SERVICE_DB_DSN` (`docker compose up -d book-db`) | `book-service/` |
| Запуск User Service | `go run ./cmd/server` (gRPC на `:8082`, REST + Swagger на `:8092`), нужна PostgreSQL (`docker compose up -d user-db`) | `user-service/` |
| Запуск Loan Service | `go run ./cmd/server` (gRPC на `:8083`, REST + Swagger на `:8093`), нужны `LOAN_SERVICE_DB_DSN` (`docker compose up -d loan-db`), запущенные book- и user-service | `loan-service/` |
| Генерация gRPC-кода | `./scripts/gen_proto.ps1` | корень (PowerShell) |
| Полный стек в Docker | `docker compose up -d --build` (book/user/loan-service + book-db `:5434` + user-db `:5432` + loan-db `:5435` + pgvector `:5433` + RabbitMQ `:5672`/`:15672`), `docker compose down -v` | корень |
| Инфраструктура без сервисов | `docker compose up -d rabbitmq user-db book-db loan-db ai-rag-db` | корень |

Текущее состояние проверок (последний запуск): build/vet — чисто во всех модулях; тесты — `book-service` (домен, сервис, postgres-репозиторий против PostgreSQL, HTTP-слой), `user-service` (49 тестов + 127 подтестов: домен, security, HTTP-слой) и `loan-service` (домен, auth-интерцептор, сервис с моками, postgres-репозиторий против PostgreSQL, HTTP-слой). Pg-тесты `book-service` и `loan-service` пропускаются без `POSTGRES_TEST_DSN`; локально — `docker compose up -d book-db loan-db`, затем `POSTGRES_TEST_DSN='host=localhost port=5434 user=library password=library dbname=library_books sslmode=disable'` (book) и `... port=5435 ... dbname=library_loans ...` (loan).

Нюанс с `gofmt -l`: в рабочем дереве файлы `book-service/*` и `pkg/config`, `pkg/logger` идут с **CRLF** (включён `core.autocrlf=true`), поэтому `gofmt -l` помечает их все, хотя содержимое отформатировано корректно (`gofmt -d` показывает различие только в концах строк). Файлы, созданные с LF (`user-service/*`, `loan-service/*`, `pkg/migrate`), помечены не быть. Гонять `gofmt -w .` ради этого не нужно — это перепишет конца строк во всех файлах модуля; форматировать стоит точечно, в файлах где реально менялся код.

Окружение: `go1.27.1 windows/amd64`; `protoc 36.2` лежит локально в `tools/protoc/bin/protoc.exe` (в системном PATH его нет); плагины `protoc-gen-go`, `protoc-gen-go-grpc`, `protoc-gen-grpc-gateway`, `protoc-gen-openapiv2` установлены в `C:\Users\ThinkPro\go\bin`. GOPROXY доступен, Docker CLI есть, демон запущен. `gh` CLI v2.102.0 (авторизован, PATH через System).

## Правила разработки

**Структура сервиса (проверяемое правило).** Каждый сервис — самостоятельный Go-модуль с фиксированным слоем:

- `cmd/server/main.go` — только сборка зависимостей и запуск; бизнес-логика здесь запрещена.
- `internal/domain/` — сущности и доменные ошибки, без внешних зависимостей.
- `internal/repository/` — доступ к PostgreSQL, единственное место с SQL/ORM.
- `internal/service/` — бизнес-правила, оркестрация репозиториев и клиентов других сервисов.
- `internal/handler/` — транспортный слой (HTTP и/или gRPC), маппинг между внешними контрактами и доменом.
- `proto/` — `.proto`-контракты сервиса.
- `migrations/` — SQL-миграции базы этого сервиса.

**Границы модулей.** Общая логика (logger, config) выносится в `pkg/`. Чужой `internal/` импортировать нельзя — межсервисные вызовы идут только через gRPC-контракты из `proto/` или через события.

**Изоляция данных.** Каждый сервис владеет своей PostgreSQL-схемой. Прямые запросы к чужой базе запрещены; согласованность между сервисами достигается событиями (`loan.issued`, `loan.returned`, `loan.overdue`).

**Порты.** Зафиксированы таблицей сервисов и не должны меняться без обновления документации: 8080 gateway, 8081 book, 8082 user, 8083 loan, 8084 notification. Исключение — вспомогательный HTTP у Book Service (`8091` = gRPC-порт + 10): REST и Swagger для разработки и ручной проверки.

**Стиль кода.** Стандартные инструменты экосистемы Go: `gofmt`/`goimports` для форматирования, `go vet` для анализа, стандартная структура имён и ошибок Go. Имена пакетов — строчные, без подчёркиваний; имена директорий — kebab-case (`book-service`). **Комментарии в коде — только на английском** (в `.go`, `.sql`, `.proto`, `.ps1`); документация (`README.md`, `KODA.md`, `*/README.md`) — на русском. Кириллица в строковых литералах допустима, когда это осмысленные тестовые данные (например, негативные кейсы валидации: `"Иван Петров"` должен быть отклонён).

**Тестирование.** Используется стандартный `testing` (без testify). Unit-тесты живут рядом с кодом (`*_test.go`, пакет `*_test`). В `book-service` домен и HTTP-слой проверяются без БД (`httptest`), а сервис и репозиторий — против PostgreSQL через `internal/testdb` (миграции, advisory-lock, truncate): без `POSTGRES_TEST_DSN` эти тесты пропускаются. В `user-service` покрыты домен, security (PR #11) и HTTP-слой (PR #26). В `loan-service` покрыты домен, auth-интерцептор, сервис (с моками репозитория/book-клиента/паблишера) и HTTP-слой (`httptest`), а репозиторий — против PostgreSQL через `internal/testdb`. TODO: тесты репозитория user-service против PostgreSQL (тестовые контейнеры), интеграционный тест gRPC-handler'а через `bufconn`, моки для gRPC-клиентов.

**Конфигурация.** Переменные окружения, читаются через `pkg/config` (значения по умолчанию задаются в `cmd/server/main.go`). Префикс Book Service — `BOOK_SERVICE_*` (см. таблицу в `book-service/README.md`).

**Актуализация главного README.md (обязательная).** Корневой `README.md` — архитектурная спецификация проекта и источник истины для внешних читателей. При выполнении **любой** задачи, затрагивающей состояние системы (новая фича, исправление, перенос артефактов, изменение инфраструктуры), **всегда** проверяй и при необходимости обновляй секции главного README.md, описывающие затронутый компонент. Устаревшее описание хуже, чем отсутствие описания: вводит в заблуждение.

**Git-процесс (обязательный).** Работа ведётся только через ветки и pull request'ы в `main`:

- Для каждой задачи из GitHub Issues заводится **отдельная ветка** от актуального `main`, имя — `<тип>/<номер-задачи>-<краткое имя>` (например, `test/2-user-domain-security`, `feature/12-postgres-book-repo`). Тип: `test`, `feature`, `fix`, `ci`, `infra`, `arch`, `docs`.
- Одна ветка — одна задача. В чужие ветки и в чужие задачи не лезть; несвязанные замечания выносить в отдельные задачи.
- Коммиты — атомарные, сообщения на английском. В описании PR указать `Closes #<номер>` (или `Fixes #`), чтобы issue закрывался автоматически.
- По завершении задачи создаётся **pull request в `main`** (`gh pr create`). PR проверяется: `go build ./...`, `go vet ./...`, `go test ./...` в затронутых модулях — до открытия PR.
- **Ревью и мерж PR делает только владелец репозитория (sapelyuk).** AI не мержит PR, не пушит прямо в `main`, не делает force-push и не переписывает историю общих ветков без явного запроса.
- Пуш своей рабочей ветки в `origin` разрешён (для создания PR). `git push` в `main` запрещён всегда.

**Актуализация `KODA.md`.** Файл — живой документ и **правится по ходу выполнения тикетов**: если в работе всплывает момент, требующий обновления (новое соглашение, нюанс окружения, изменение процесса, уточнение архитектуры), правка делается в ветке текущего тикета вместе с кодом. Если изменение самодостаточно и объёмно — заводится отдельный тикет с типом `docs`. Устаревшие сведения удаляются, а не дублируются: файл должен отражать фактическое состояние, а не историю.

## Открытые вопросы для уточнения

- Определить, какие эндпоинты gateway'я публичные (REST) и какие внутренние (gRPC).
- Покрыть тестами репозитории против PostgreSQL и интеграционные gRPC-тесты; HTTP-слои user/book закрыты в PR #26/#27.

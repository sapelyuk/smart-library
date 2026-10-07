# Loan Service

Микросервис учёта выдачи книг: кто взял какой экземпляр, до какого срока и
когда вернул. Основной транспорт — gRPC (`loan.v1.LoanService`), порт по
умолчанию **:8083**. Дополнительно сервис поднимает HTTP-слой (**:8093**):
REST-ручки из аннотаций `google.api.http` (grpc-gateway) и Swagger UI для
ручного теста этих же ручек в браузере.

## Архитектура

```
loan-service/
├── cmd/server/            # main: конфигурация, запуск gRPC + HTTP, graceful shutdown
├── proto/loan/v1/         # контракт API (protobuf) + аннотации google.api.http
├── gen/go/                # сгенерированный код (protoc-gen-go, -go-grpc, -grpc-gateway)
├── docs/                  # loan/v1/loan.swagger.json (генерация) + embed спецификации
├── internal/
│   ├── domain/            # Loan, LoanStatus, правила срока/продления, Principal, ошибки
│   ├── service/           # бизнес-логика (use-case'ы)
│   ├── repository/        # порт хранилища + PostgreSQL-реализация (pgx)
│   ├── bookclient/        # gRPC-клиент book-service (резерв/освобождение экземпляра)
│   ├── usersvc/           # gRPC-клиент user-service (проверка bearer-токена)
│   ├── auth/              # интерцептор: токен -> Principal в контексте
│   ├── events/            # порт событий (LogPublisher до появления pkg/events)
│   ├── testdb/            # провижининг тестовой БД (миграции, advisory-lock)
│   └── handler/           # grpc.go: прото <-> домен; http.go: gateway + Swagger UI
└── migrations/            # SQL-схема PostgreSQL + embed для pkg/migrate
```

Зависимости направлены строго внутрь: `handler -> service -> repository -> domain`.
Доменные ошибки (`domain.ErrNotFound`, `domain.ErrAlreadyReturned`,
`domain.ErrPermissionDenied`, ...) переводятся в коды gRPC в `internal/handler`
и не текут наружу как `Internal`.

### Взаимодействие с другими сервисами

Сервис не владеет каталогом: он **резервирует** экземпляр в book-service перед
записью выдачи и **освобождает** его при возврате. Если запись выдачи не
удалась, зарезервированный экземпляр возвращается в инвентарь, чтобы копия не
«зависла» навсегда.

- `bookclient.BorrowCopy(book_id)` → `BorrowBookCopy` в book-service, берёт
  первый доступный экземпляр и возвращает его `copy_id`;
- `bookclient.ReturnCopy(copy_id)` → `ReturnBookCopy` в book-service;
- `usersvc.Verifier.Verify(token)` → `AuthenticateToken` в user-service:
  резолвит bearer-токен в `Principal` (id + роль).

Оба соединения устанавливаются на старте; недоступность book-service или
user-service — фатальная ошибка запуска.

## Домен и правила

- **Loan** — агрегат: `reader_id`, `book_id`, `copy_id`, `borrowed_at`,
  `due_at`, `returned_at`, `status`.
- **`LoanStatus`** хранит только `ACTIVE` и `RETURNED`. `OVERDUE` **не
  хранится**: это функция `due_at` и `returned_at` (`Loan.EffectiveStatus`),
  иначе потребовался бы фоновый процесс для поддержания статуса в актуальном
  состоянии.
- Срок выдачи по умолчанию — 14 дней (`LOAN_SERVICE_LOAN_PERIOD`), границы
  периода проверяются в домене.
- Продление сдвигает `due_at` вперёд; возвращённую выдачу продлить нельзя.
- **Права**: читатель оперирует только своими выдачами; библиотекарь — любыми.
  Проверки в `domain.Principal` (`RequireBorrowAccess`, `RequireLoanAccess`).
  Список выдач читателя всегда принудительно ограничен его `user_id`, что бы
  ни было в фильтре запроса.

## Хранилище

**PostgreSQL 17** через `github.com/jackc/pgx/v5/stdlib` (драйвер
`database/sql`). Схема применяется на старте через `pkg/migrate`
(`LOAN_SERVICE_DB_MIGRATE=true`), миграции встроены в бинарник (`//go:embed`).
Хранилища в памяти у сервиса нет: `LOAN_SERVICE_DB_DSN` обязателен, без него
процесс завершается с ошибкой.

Схема (`migrations/001_init.sql`) держит инварианты на уровне БД:

- `CHECK (status IN ('ACTIVE', 'RETURNED'))` и `CHECK (due_at > borrowed_at)`;
- **частичный уникальный индекс** `loans_active_copy_idx ON loans (copy_id)
  WHERE returned_at IS NULL` — один экземпляр не может быть выдан дважды
  одновременно, но полная история выдач сохраняется;
- `reader_id`, `book_id`, `copy_id` — обычные UUID **без внешних ключей**:
  эти строки живут в базах user-service и book-service (database-per-service,
  кросс-базовый FK невозможен);
- индексы под выборки «выдачи читателя» (`borrowed_at DESC`), «выдачи книги» и
  overdue-скан (`due_at WHERE returned_at IS NULL`).

## Запуск

```bash
cd loan-service
export LOAN_SERVICE_DB_DSN='host=localhost port=5435 user=library password=library dbname=library_loans sslmode=disable'
export LOAN_SERVICE_BOOK_SERVICE_GRPC_ADDR='localhost:8081'
export LOAN_SERVICE_USER_SERVICE_GRPC_ADDR='localhost:8082'
go run ./cmd/server
```

Переменные окружения:

| Переменная | По умолчанию | Назначение |
| --- | --- | --- |
| `LOAN_SERVICE_GRPC_ADDR` | `:8083` | адрес gRPC-сервера |
| `LOAN_SERVICE_HTTP_ADDR` | `:8093` | адрес REST/Swagger-сервера |
| `LOAN_SERVICE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `LOAN_SERVICE_LOG_FORMAT` | `json` | `json` или `text` |
| `LOAN_SERVICE_SHUTDOWN_TIMEOUT` | `15s` | таймаут graceful shutdown |
| `LOAN_SERVICE_DB_DSN` | — | **обязателен**, DSN PostgreSQL |
| `LOAN_SERVICE_DB_MIGRATE` | `true` | применять миграции при старте |
| `LOAN_SERVICE_DB_MAX_OPEN_CONNS` | `25` | максимум открытых соединений |
| `LOAN_SERVICE_DB_MAX_IDLE_CONNS` | `5` | максимум простаивающих соединений |
| `LOAN_SERVICE_DB_CONN_MAX_LIFETIME` | `30m` | время жизни соединения |
| `LOAN_SERVICE_BOOK_SERVICE_GRPC_ADDR` | — | **обязателен**, адрес book-service |
| `LOAN_SERVICE_USER_SERVICE_GRPC_ADDR` | — | **обязателен**, адрес user-service |
| `LOAN_SERVICE_LOAN_PERIOD` | `336h` (14 дней) | срок выдачи нового loan |

Останов по `Ctrl+C` — graceful shutdown: сначала снимается health-статус
`SERVING`, закрывается HTTP-сервер, затем `GracefulStop` с таймаутом.

## REST API и Swagger

HTTP-слой построен на grpc-gateway: маршруты берутся из аннотаций
`google.api.http` в `proto/loan/v1/loan.proto`, поэтому REST и gRPC — это
один и тот же контракт и одна и та же бизнес-логика. Запросы приходят на
`:8093` и проксируются в gRPC-сервер этого же процесса (`:8083`).

Документация: **http://localhost:8093/swagger/** (корень `/` редиректит туда
же), спецификация — http://localhost:8093/swagger/swagger.json. Ассеты Swagger
UI подключаются с CDN, поэтому для открытия страницы нужен доступ в интернет;
сама спека отдаётся из бинарника (`//go:embed`).

Все методы требуют заголовок `authorization: Bearer <token>` с токеном,
полученным из user-service (`Login`).

Liveness: **`GET /healthz`** → `200 OK`. Эндпоинт обслуживает HTTP-сервер
рядом с REST-слоем и используется healthcheck'ом контейнера.

| Метод | Путь | RPC |
| --- | --- | --- |
| `POST` | `/v1/loans` | `Borrow` (`reader_id`, `book_id` в теле) |
| `POST` | `/v1/loans/{loan_id}/return` | `Return` (тела нет, `loan_id` в URL) |
| `POST` | `/v1/loans/{loan_id}/renew` | `Renew` (`extend_days` в теле) |
| `GET` | `/v1/loans/{loan_id}` | `Get` |
| `GET` | `/v1/loans` | `List` (`reader_id`, `book_id`, `status`, `limit`, `offset`) |
| `GET` | `/v1/loans/overdue` | `ListOverdue` (`limit`, `offset`) |

Доменные ошибки переводятся в HTTP-коды так же, как в gRPC-коды:
`400` (невалидные данные, `FailedPrecondition` — например, повторный возврат),
`401` (нет/невалидный токен), `403` (`PermissionDenied`), `404`. Тело ошибки —
`{"code": ..., "message": "...", "details": []}`.

```bash
TOKEN=<токен из user-service Login>

# выдать книгу читателю (reader_id — из токена)
curl -X POST http://localhost:8093/v1/loans -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"reader_id":"<uuid>","book_id":"<uuid>"}'

# список своих выдач
curl "http://localhost:8093/v1/loans?limit=20" -H "Authorization: Bearer $TOKEN"

# продлить и вернуть (loan_id — из ответа Borrow)
curl -X POST http://localhost:8093/v1/loans/<loan_id>/renew -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" -d '{"extend_days":7}'
curl -X POST http://localhost:8093/v1/loans/<loan_id>/return -H "Authorization: Bearer $TOKEN"
```

## Методы API

| Метод | Назначение |
| --- | --- |
| `Borrow` | зарезервировать первый доступный экземпляр и записать выдачу |
| `Return` | закрыть выдачу и освободить экземпляр в book-service |
| `Renew` | сдвинуть `due_at` активной выдачи вперёд |
| `Get` | одна выдача (читателю — только своя) |
| `List` | страница выдач с фильтрами (читателю — только свои) |
| `ListOverdue` | активные выдачи с истёкшим сроком |

## События

Сервис публикует доменные события в порт `internal/events`. Реальный транспорт —
RabbitMQ (ADR-0001, `docs/adr/0001-message-broker.md`); до появления
`pkg/events` (issue #14) используется `LogPublisher`, который пишет события в
лог. Имена (routing keys) уже зафиксированы:

| Событие | Когда |
| --- | --- |
| `loan.issued` | выдача записана |
| `loan.returned` | выдача закрыта |
| `loan.overdue` | зарезервировано под overdue-скан (фонового процесса пока нет) |

Публикация — best effort: падение брокера логируется и не превращает успешную
выдачу в ошибку.

## Примеры (grpcurl)

На сервере включены reflection и standard health check. Приватные методы
требуют metadata `authorization`:

```bash
# health
grpcurl -plaintext localhost:8083 grpc.health.v1.Health/Check

# выдать книгу
grpcurl -plaintext \
  -H 'authorization: Bearer <token>' \
  -d '{"reader_id": "<uuid>", "book_id": "<uuid>"}' \
  localhost:8083 loan.v1.LoanService/Borrow

# список своих выдач
grpcurl -plaintext -H 'authorization: Bearer <token>' -d '{"limit": 20}' \
  localhost:8083 loan.v1.LoanService/List

# вернуть
grpcurl -plaintext -H 'authorization: Bearer <token>' -d '{"loan_id": "<uuid>"}' \
  localhost:8083 loan.v1.LoanService/Return
```

## Генерация кода

Контракт — `proto/loan/v1/loan.proto` (он же источник REST-маршрутов и Swagger).
После его изменения:

```powershell
./scripts/gen_proto.ps1
```

Скрипт запускает `protoc` из `tools/protoc` и четыре плагина:

| Плагин | Что генерирует |
| --- | --- |
| `protoc-gen-go` | `gen/go/loan/v1/loan.pb.go` |
| `protoc-gen-go-grpc` | `gen/go/loan/v1/loan_grpc.pb.go` |
| `protoc-gen-grpc-gateway` | `gen/go/loan/v1/loan.pb.gw.go` (REST-прокси) |
| `protoc-gen-openapiv2` | `docs/loan/v1/loan.swagger.json` |

## Разработка

```bash
cd loan-service
go build ./...
go vet ./...
go test ./...
```

Тесты репозитория требуют PostgreSQL: при отсутствии переменной
`POSTGRES_TEST_DSN` они пропускаются. Локально:

```bash
# поднять loan-db из корня репозитория
docker compose up -d loan-db
# в PowerShell
$env:POSTGRES_TEST_DSN='host=localhost port=5435 user=library password=library dbname=library_loans sslmode=disable'
go test ./...
```

Схема тестовой БД накатывается автоматически (`internal/testdb`), таблицы
очищаются перед каждым тестом. В CI база поднимается сервис-контейнером
`postgres:17` и тот же DSN задаётся в переменной окружения.

Покрытие: домен, auth-интерцептор, сервис (с моками репозитория/клиента
book-service/паблишера), PostgreSQL-репозиторий против реальной БД и HTTP-слой
через `httptest`.

## Ограничения текущей версии

- События (RabbitMQ) — заглушка `LogPublisher` до реализации `pkg/events` (#14).
- Нет фонового overdue-скана: `ListOverdue` вычисляет просрочку на лету,
  событие `loan.overdue` пока не публикуется.
- API-gateway ещё не маршрутизирует `/v1/loans/*` на этот сервис.

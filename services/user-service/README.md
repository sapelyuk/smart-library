# User Service

Микросервис учётных записей и аутентификации: читатели (`READER`) и
библиотекари (`LIBRARIAN`), регистрация, вход с bearer-токеном, управление
пользователями по RBAC-правилам. Основной транспорт — gRPC
(`user.v1.UserService`), порт по умолчанию **:8082**. Дополнительно сервис
поднимает HTTP-слой (**:8092**): REST-ручки из аннотаций `google.api.http`
(grpc-gateway) и Swagger UI.

В отличие от Book Service, хранилище здесь сразу **PostgreSQL** (Docker,
PG 17): сессии и пароли не должны жить в памяти процесса.

## Архитектура

```
user-service/
├── cmd/server/            # main: env-конфиг, миграции, сид библиотекаря, gRPC + HTTP
├── proto/user/v1/         # контракт API (protobuf) + аннотации google.api.http
├── gen/go/                # сгенерированный код (protoc-gen-go, -go-grpc, -grpc-gateway)
├── docs/                  # user/v1/user.swagger.json (генерация) + embed спецификации
├── internal/
│   ├── domain/            # User, Role, UserStatus, Email, пароли, Session, Principal (RBAC)
│   ├── security/          # argon2id-хеширование (PHC-формат), генерация/хеширование токенов
│   ├── events/            # порт событий (Publisher) и типы/полезные нагрузки событий
│   ├── consumer/          # консьюмер-заготовка уведомлений (до Notification Service, #48)
│   ├── service/           # use-case'ы: регистрация, вход, RBAC-проверки, отзыв сессий
│   ├── repository/        # порты UserRepository/SessionRepository
│   │   └── postgres/      # реализация на database/sql + lib/pq
│   └── handler/           # grpc-адаптер, auth-интерцептор, gateway + Swagger UI
├── migrations/            # 001_init.sql (goose-формат) + go:embed
└── migrations.go          # FS для pkg/migrate
```

Зависимости направлены строго внутрь: `handler -> service -> repository -> domain`.
Доменные ошибки (`domain.ErrNotFound`, `domain.ErrUnauthenticated`, ...)
переводятся в коды gRPC в `internal/handler` и не текут наружу как `Internal`.

## Модель доступа

- **Токен** — 32 случайных байта (base64url). В базе хранится только его
  SHA-256-хеш; украденная база не выдаёт действующие токены.
- **Сессия** живёт `USER_SERVICE_SESSION_TTL` (по умолчанию 24 ч). Вход создаёт
  новую сессию, `Logout` отзывает текущую, смена пароля отзывает **все**
  остальные сессии пользователя.
- **READER** — роль, которую получает каждый зарегистрированный. Дальше её
  может повысить только библиотекарь (`PATCH /v1/users/{id}`).
- **RBAC** (в `internal/domain/principal.go`): список пользователей, создание,
  деактивация/восстановление и смена ролей — только для библиотекаря;
  карточку и смену парола пользователь может запросить на себя; библиотекарь
  не может деактивировать или понизить сам себя (защита от самоблокировки).
- **Пароли** — argon2id (`m=19456, t=2, p=1`, код в PHC-формате). Политика:
  минимум `USER_SERVICE_PASSWORD_MIN_LENGTH` (12) символов и не менее трёх
  классов символов. Вход сравнивает пароль с фиктивной хеш-функцией, когда
  аккаунта нет, — время ответа не выдаёт существование e-mail.

## Хранилище

PostgreSQL через `database/sql` + `lib/pq`. Оба драйвера покрывают потребности миграции и репозитория; переход на `pgx` — отдельная задача (Issue #32). UUID генерирует приложение намеренно (см. `migrations/README.md`).

Схема (`migrations/001_init.sql`): таблица `users` (уникальный `email`),
таблица `sessions` (`token_hash` уникальный, `expires_at`), таблица
`schema_migrations` для учёта применённого. Миграции применяются при старте
(`pkg/migrate`); отключаются `USER_SERVICE_DB_MIGRATE=false`.

Шапка `001_init.sql` описывает колонки как след поддержки PostgreSQL 9.3 — это
история написания файла, а не описание текущей схемы: сервер — `postgres:17`, а
сам файл неизменяем из-за сверки checksum. Актуальное обоснование типов — в
`migrations/README.md` и в `COMMENT ON` из `002_document_schema.sql`.

### База в Docker

Сервис подключается к PostgreSQL из `docker-compose.yml` (контейнер
`library-user-db`, порт хоста `5432`). Схема не требуется — сервис применит
миграции сам при первом запуске.

```powershell
# поднимает user-db на localhost:5432
docker compose up -d user-db
```

DSN для `.env`: `host=127.0.0.1 port=5432 user=library password=library dbname=library_users sslmode=disable`.

## События

Сервис публикует доменные события в RabbitMQ через общий модуль `pkg/events`
(транспорт — `pkg/events/amqp`, ADR-0001). Событие уходит в topic exchange
`library.events` (переменная `RABBITMQ_EXCHANGE`) под routing key, равным типу
события; тело — JSON-конверт `{event_id, event_type, occurred_at,
schema_version, payload}`.

| Тип события | Когда публикуется | payload |
| --- | --- | --- |
| `user.registered` | создан аккаунт (`Register` или `CreateUser`) | `user_id`, `email`, `role`, `status` |
| `user.status.changed` | аккаунт активирован/заблокирован | `user_id`, `email`, `role`, `status` |
| `user.session.created` | вход выдал сессию | `user_id`, `session_id`, `expires_at` |

Публикация — best effort: ошибка брокера логируется, но не отменяет бизнес-операцию
(аккаунт уже записан). Транспорт соединяется лениво и переподключается сам, поэтому
недоступный на старте RabbitMQ не мешает сервису подняться.

Пока нет отдельного Notification Service (Issue #48), роль консьюмера-заготовки
играет `internal/consumer`: он читает `user.#` из durable-очереди
`user.notifications.stub` и логирует события. Это держит путь подписки
(привязка, ручной `ack`, переподключение) рабочим до появления настоящего сервиса.

## Запуск

Перед запуском скопируйте корневой `.env.example` в `.env` и заполните реальные
значения (файл `.env` не коммитится):

```powershell
Copy-Item .env.example .env
```

Затем экспортируйте переменные в шелл или запустите сервис через IDE.

```bash
cd user-service
go run ./cmd/server
```

Перед первым запуском нужен `librarian` — его создаёт сид (см. ниже), либо
`POST /v1/users` от уже существующего библиотекаря.

Переменные окружения:

| Переменная | По умолчанию | Назначение |
| --- | --- | --- |
| `USER_SERVICE_GRPC_ADDR` | `:8082` | адрес gRPC-сервера |
| `USER_SERVICE_HTTP_ADDR` | `:8092` | адрес REST/Swagger-сервера |
| `USER_SERVICE_DB_DSN` | *(обязательна)* | строка подключения PostgreSQL (Docker: `localhost:5432`) |
| `USER_SERVICE_DB_MIGRATE` | `true` | применять миграции при старте |
| `RABBITMQ_URL` | `amqp://guest:guest@localhost:5672/` | AMQP-строка подключения брокера |
| `RABBITMQ_EXCHANGE` | `library.events` | topic exchange доменных событий |
| `USER_SERVICE_SESSION_TTL` | `24h` | срок жизни bearer-токена |
| `USER_SERVICE_PASSWORD_MIN_LENGTH` | `12` | минимальная длина пароля |
| `USER_SERVICE_PURGE_INTERVAL` | `15m` | как часто удалять просроченные сессии |
| `USER_SERVICE_SEED_LIBRARIAN_EMAIL` | *(пусто)* | e-mail первого библиотекаря |
| `USER_SERVICE_SEED_LIBRARIAN_PASSWORD` | *(пусто)* | его пароль |
| `USER_SERVICE_SEED_LIBRARIAN_FULL_NAME` | `Head Librarian` | имя в карточке сида |
| `USER_SERVICE_SEED_LIBRARIAN_PHONE` | *(пусто)* | телефон в карточке сида |
| `USER_SERVICE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `USER_SERVICE_LOG_FORMAT` | `json` | `json` или `text` |
| `USER_SERVICE_SHUTDOWN_TIMEOUT` | `15s` | таймаут graceful shutdown |

Сид библиотекаря — единственный способ завести первого администратора:
`CreateUser` требует библиотекаря, поэтому без сида система регистрировалась бы,
но не администрировалась. Сид идемпотентен: если e-mail уже занят, сервис
просто продолжает старт. В продакшене переменные сида оставляют пустыми.

Останов по `Ctrl+C` — graceful shutdown: снимается health-статус `SERVING`,
закрывается HTTP-сервер, затем `GracefulStop` с таймаутом.

## REST API и Swagger

HTTP-слой построен на grpc-gateway: маршруты берутся из аннотаций
`google.api.http` в `proto/user/v1/user.proto`, поэтому REST и gRPC — один
контракт и одна бизнес-логика. Запросы приходят на `:8092` и проксируются в
gRPC-сервер этого же процесса (`:8082`).

Документация: **http://localhost:8092/swagger/** (корень `/` редиректит туда
же), спецификация — http://localhost:8092/swagger/swagger.json. Ассеты
Swagger UI подключаются с CDN; спека отдаётся из бинарника (`//go:embed`).

Liveness: **`GET /healthz`** → `200 OK`. Эндпоинт обслуживает HTTP-сервер
рядом с REST-слоем и используется healthcheck'ом контейнера.

Авторизация — заголовок `Authorization: Bearer <accessToken>` из ответа
`login`. Публичные ручки — только `register` и `login`; `AuthenticateToken`
существует для внутренних вызовов (будущие gateway/loan) и в REST не
аннотирован.

| Метод | Путь | RPC | Кто |
| --- | --- | --- | --- |
| `POST` | `/v1/auth/register` | `Register` | все |
| `POST` | `/v1/auth/login` | `Login` | все |
| `POST` | `/v1/auth/logout` | `Logout` | владелец токена |
| `GET` | `/v1/auth/me` | `GetCurrentUser` | владелец токена |
| `POST` | `/v1/users` | `CreateUser` | библиотекарь |
| `GET` | `/v1/users` | `ListUsers` (`role`, `status`, `query`, `pageSize`, `pageToken`) | библиотекарь |
| `GET` | `/v1/users/{id}` | `GetUser` | себя — любой, остальных — библиотекарь |
| `PATCH` | `/v1/users/{id}` | `UpdateUser` | контакты — себе; роль/статус — библиотекарю |
| `POST` | `/v1/users/{id}/password` | `ChangePassword` | себе — любой, любому — библиотекарю |
| `POST` | `/v1/users/{id}/deactivate` | `DeactivateUser` | библиотекарь (не себе) |
| `POST` | `/v1/users/{id}/restore` | `RestoreUser` | библиотекарь |

Ошибки: `400` невалидные данные, `401` нет/просрочен токен или неверный
пароль, `403` роль не позволяет, `404` нет пользователя, `409` e-mail занят.
Тело — `{"code": ..., "message": "...", "details": []}`.

```bash
# регистрация читателя (рость READER выдаётся сама)
curl -X POST http://localhost:8092/v1/auth/register -H "Content-Type: application/json" \
  -d '{"email":"reader@library.local","password":"ReaderPass123!","full_name":"Ivan Reader"}'

# вход — в ответе accessToken
curl -X POST http://localhost:8092/v1/auth/login -H "Content-Type: application/json" \
  -d '{"email":"reader@library.local","password":"ReaderPass123!"}'

# свой профиль
curl http://localhost:8092/v1/auth/me -H "Authorization: Bearer <token>"

# список пользователей — только библиотекарь (иначе 403)
curl "http://localhost:8092/v1/users?role=READER&pageSize=20" -H "Authorization: Bearer <librarian_token>"

# выход — токен перестаёт действовать
curl -X POST http://localhost:8092/v1/auth/logout -H "Authorization: Bearer <token>"
```

## Методы API

| Метод | Назначение |
| --- | --- |
| `Register` | завести читателя (пароль по политике, роль `READER`) |
| `Login` | выдать bearer-токен (новая сессия с TTL) |
| `Logout` | отозвать текущую сессию |
| `GetCurrentUser` | карточка по токену |
| `AuthenticateToken` | внутренняя проверка токена для других сервисов |
| `CreateUser` | библиотекарь заводит пользователя с любой ролью |
| `GetUser` | карточка пользователя |
| `ListUsers` | список с фильтрами `role`/`status`/`query`, курсорная пагинация |
| `UpdateUser` | частичное обновление (роль/статус — только библиотекарь) |
| `ChangePassword` | смена пароля; отзывает все прочие сессии |
| `DeactivateUser` | заблокировать (`ACTIVE -> DEACTIVATED`, вход невозможен) |
| `RestoreUser` | разблокировать (`DEACTIVATED -> ACTIVE`) |

## Примеры (grpcurl)

На сервере включены reflection и standard health check:

```bash
# health
grpcurl -plaintext localhost:8082 grpc.health.v1.Health/Check

# вход библиотекаря
grpcurl -plaintext -d '{"email":"librarian@library.local","password":"LibrarianPass1!"}' \
  localhost:8082 user.v1.UserService/Login

# свой профиль с токеном
grpcurl -plaintext -H "authorization: Bearer <token>" \
  localhost:8082 user.v1.UserService/GetCurrentUser

# завести библиотекаря (токен библиотекаря обязателен)
grpcurl -plaintext -H "authorization: Bearer <token>" -d '{
  "email": "second@library.local",
  "password": "AnotherPass123!",
  "full_name": "Second Librarian",
  "role": "ROLE_LIBRARIAN"
}' localhost:8082 user.v1.UserService/CreateUser
```

## Генерация кода

Контракт — `proto/user/v1/user.proto` (он же источник REST-маршрутов и
Swagger). После его изменения:

```powershell
./scripts/gen_proto.ps1
```

Скрипт перегенерирует и book-service, и user-service: `*.pb.go`,
`*_grpc.pb.go`, `*.pb.gw.go` и `docs/user/v1/user.swagger.json`.

## Разработка

```bash
cd user-service
go build ./...
go vet ./...
go test ./...
```

## Ограничения текущей версии

- Покрыты домен, security и HTTP-слой; сервис и postgres-слой пока без тестов —
  проверяются ручной проверкой живого API.
- Драйвер `lib/pq` вместо `pgx` — на PG 17 переход разблокирован (Issue #32).
- Нет аутентификации mTLS между сервисами: `AuthenticateToken` рассчитан на
  доверенную сеть.
- Консьюмер уведомлений — заготовка (`internal/consumer`): реальная доставка
  появится в Notification Service (Issue #48).

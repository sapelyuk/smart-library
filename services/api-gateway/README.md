# API Gateway

**API Gateway** — единый HTTP-вход для клиентских приложений Smart Library.
Шлюз проверяет JWT-токен, маршрутизирует запросы к `book-service` и `user-service`
по префиксу пути и добавляет заголовок `X-Forwarded-For`.

```
Клиент (HTTP/JSON)
        │  Authorization: Bearer <token>
        ▼
┌─────────────────────────────────────────┐
│  API Gateway :8080                       │
│  JWT-аутентификация · маршрутизация      │
├────────────────┬────────────────────────┤
│  /v1/books/*   │  /v1/users/* /v1/auth/ │
│  /v1/borrow/*  │                        │
▼                ▼                        │
│  book-service :8091  │  user-service :8092│
└──────────────────────────────────────────┘
```

## Архитектура

Gateway — **reverse-proxy** на стандартной библиотеке Go:

- **Маршрутизация** по префиксу URL в `http.ServeMux`:
  - `/v1/books/*`, `/v1/borrow/*` → `book-service` (HTTP :8091)
  - `/v1/users/*`, `/v1/auth/*` → `user-service` (HTTP :8092)
- **Аутентификация** — middleware, проверяет Bearer-токен:
  - Публичные пути (`/v1/auth/login`, `/v1/auth/register`) — без JWT
  - Остальные пути — требуют `Authorization: Bearer <token>`
- **Авторизация** — middleware `RequireRole()`:
  - `READER` — чтение каталога, borrow/return
  - `LIBRARIAN` — CRUD книг, управление пользователями
  - `ADMIN` — полный доступ

## Запуск

### Docker Compose (локальная разработка)

```bash
docker compose up -d api-gateway
```

Gateway подключается к `book-service` и `user-service` по их Docker-именам:
`book-service:8091` и `user-service:8092`.

### Локальный запуск (из шелла)

```bash
cd services/api-gateway
export BOOK_SERVICE_URL=http://localhost:8091
export USER_SERVICE_URL=http://localhost:8092
go run ./cmd/server
```

### Переменные окружения

| Переменная | По умолчанию | Назначение |
| --- | --- | --- |
| `HTTP_ADDR` | `:8080` | адрес HTTP-шлюза |
| `BOOK_SERVICE_URL` | *(обязательна)* | HTTP-адрес book-service (grpc-gateway) |
| `USER_SERVICE_URL` | *(обязательна)* | HTTP-адрес user-service (grpc-gateway) |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `LOG_FORMAT` | `json` | `json` или `text` |
| `SHUTDOWN_TIMEOUT` | `15s` | таймаут graceful shutdown |
| `SHARE_SECRET` | `smart-library-dev-secret` | секрет для проверки JWT-подписи |

## Маршрутизация

| Путь       | Сервис         | Порт | Авторизация |
| ---        | ---            | ---  | ---       |
| `/v1/books/*` | book-service | 8091 | да |
| `/v1/borrow/*` | book-service | 8091 | да |
| `/v1/users/*` | user-service | 8092 | да |
| `/v1/auth/login` | user-service | 8092 | нет |
| `/v1/auth/register` | user-service | 8092 | нет |
| `/v1/auth/logout` | user-service | 8092 | да |
| `/v1/auth/me` | user-service | 8092 | да |
| `/healthz` | — | — | нет |

## Безопасность

- JWT-токены проверяются через HMAC-SHA256 с секретом `SHARE_SECRET`.
- Секрет должен совпадать на Gateway и User Service.
- Публичные пути (`login`, `register`) не требуют JWT.
- Все запросы к Gateway проходят через аутентификатор.
- Заголовок `X-Forwarded-For` добавляется для каждого запроса.

## Структура

```
api-gateway/
├── cmd/server/main.go     # main: env-конфиг, маршрутизация, HTTP-сервер
├── internal/
│   ├── auth/              # JWT-аутентификация и middleware
│   │   ├── auth.go        # Authenticator, RequireRole, Claims
│   │   └── jwt.go         # HMAC-SHA256 проверка, base64url декодирование
│   └── proxy/             # обратный прокси к book/user сервисам
│       └── proxy.go       # httputil.ReverseProxy, Director
├── proto/                 # proto-контракт GatewayService
├── gen/go/                # сгенерированный код (не править)
├── docs/                  # Swagger спецификация (генерация)
├── Dockerfile             # multi-stage образ
└── go.mod                 # модуль
```

## Тестирование

```bash
cd services/api-gateway
go build ./...
go vet ./...
go test ./...
```

## Связь с другими сервисами

- **Book Service** (`:8091`) — маршрутизация по `/v1/books/*`, `/v1/borrow/*`
- **User Service** (`:8092`) — маршрутизация по `/v1/users/*`, `/v1/auth/*`
- **Docker Compose** — gateway зависит от book-service и user-service
- **JWT-секрет** — `SHARE_SECRET` (должен совпадать с User Service)

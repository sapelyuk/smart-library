# API Gateway

**API Gateway** — единый HTTP-вход для клиентских приложений Smart Library.
Шлюз проверяет bearer-токены через gRPC `AuthenticateToken` user-service,
маршрутизирует запросы к `book-service` и `user-service` по префиксу пути и
добавляет заголовок `X-Forwarded-For`.

```
Клиент (HTTP/JSON)
        │  Authorization: Bearer <token>
        ▼
┌─────────────────────────────────────────┐
│  API Gateway :8080                       │
│  gRPC-аутентификация · маршрутизация    │
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
- **Аутентификация** — middleware, проверяет Bearer-токен через gRPC
  `AuthenticateToken` user-service:
  - Публичные пути (`/v1/auth/login`, `/v1/auth/register`) — без токена
  - Остальные пути — требуют `Authorization: Bearer <token>`
- **Авторизация** — middleware `RequireRole()`:
  - `/v1/users` и `/v1/books` (без слэша) — только `ROLE_LIBRARIAN`
  - `/v1/users/{id}`, `/v1/books/{id}` — любой аутентифицированный пользователь

## Запуск

### Docker Compose (локальная разработка)

```bash
docker compose up -d api-gateway
```

Gateway подключается к `book-service` и `user-service` по их Docker-именам:
`book-service:8091` и `user-service:8092` (REST), `user-service:8082` (gRPC).

### Локальный запуск (из шелла)

```bash
cd services/api-gateway
export GATEWAY_BOOK_SERVICE_URL=http://localhost:8091
export GATEWAY_USER_SERVICE_URL=http://localhost:8092
export GATEWAY_USER_SERVICE_GRPC_ADDR=localhost:8082
go run ./cmd/server
```

### Переменные окружения

| Переменная | По умолчанию | Назначение |
| --- | --- | --- |
| `GATEWAY_HTTP_ADDR` | `:8080` | адрес HTTP-шлюза |
| `GATEWAY_BOOK_SERVICE_URL` | *(обязательна)* | HTTP-адрес book-service (grpc-gateway) |
| `GATEWAY_USER_SERVICE_URL` | *(обязательна)* | HTTP-адрес user-service (grpc-gateway) |
| `GATEWAY_USER_SERVICE_GRPC_ADDR` | *(обязательна)* | gRPC-адрес user-service (AuthenticateToken) |
| `GATEWAY_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `GATEWAY_LOG_FORMAT` | `json` | `json` или `text` |
| `GATEWAY_SHUTDOWN_TIMEOUT` | `15s` | таймаут graceful shutdown |

## Маршрутизация

| Путь       | Сервис         | Порт | Авторизация |
| ---        | ---            | ---  | ---       |
| `/v1/books` | book-service | 8091 | LIBRARIAN |
| `/v1/books/*` | book-service | 8091 | да |
| `/v1/borrow/*` | book-service | 8091 | да |
| `/v1/users` | user-service | 8092 | LIBRARIAN |
| `/v1/users/*` | user-service | 8092 | да |
| `/v1/auth/login` | user-service | 8092 | нет |
| `/v1/auth/register` | user-service | 8092 | нет |
| `/v1/auth/logout` | user-service | 8092 | да |
| `/v1/auth/me` | user-service | 8092 | да |
| `/healthz` | — | — | нет |

## Безопасность

- Bearer-токены проверяются через gRPC `AuthenticateToken` user-service.
- `ROLE_LIBRARIAN` требуется на точных путях `/v1/users` и `/v1/books`.
- Публичные пути (`login`, `register`) не требуют токена.
- Заголовок `X-Forwarded-For` добавляется для каждого запроса.

## Структура

```
api-gateway/
├── cmd/server/main.go     # main: env-конфиг, маршрутизация, HTTP-сервер
├── internal/
│   ├── auth/              # gRPC-аутентификация и middleware
│   │   ├── auth.go        # Authenticator, RequireRole, Principal
│   │   └── grpc.go        # Verifier: gRPC-вызов AuthenticateToken
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
- **User Service gRPC** (`:8082`) — вызов `AuthenticateToken` для верификации токенов
- **Docker Compose** — gateway зависит от book-service и user-service

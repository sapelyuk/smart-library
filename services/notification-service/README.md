# Notification Service

Микросервис уведомлений: потребляет события `loan.*` из RabbitMQ и отправляет
email-напоминания о сроках возврата книг читателям. Не имеет собственной базы
данных — только консьюмер событий и SMTP-транспорт.

## Архитектура

```
notification-service/
├── cmd/server/            # main: env-конфиг, запуск gRPC + HTTP, консьюмер
├── proto/notification/v1/ # контракт API (protobuf) + аннотации google.api.http
├── gen/go/                # сгенерированный код (protoc-gen-go, -go-grpc, -grpc-gateway)
├── docs/                  # swagger.json (генерация) + embed спецификации
├── internal/
│   ├── domain/            # Notification entity, доменные ошибки
│   ├── service/           # бизнес-логика: выбор канала доставки
│   ├── consumer/          # консьюмер loan.* из RabbitMQ
│   ├── email/             # SMTP-транспорт
│   └── handler/           # grpc.go: прото <-> домен; http.go: gateway + Swagger UI
└── Dockerfile             # multi-stage образ
```

Сервис не имеет БД и не требует PostgreSQL. Он подключается только к RabbitMQ
и SMTP-серверу.

### Потребление событий

Сервис подписывается на queue `notification-service.loan-events`, привязанную
к topic exchange `library.events` с routing key `loan.#`. Получает события:

| Событие | Когда | payload |
| --- | --- | --- |
| `loan.issued` | выдача записана | `loan_id`, `reader_id`, `book_title`, `book_author`, `due_at`, `reader_email`, `reader_phone` |
| `loan.overdue` | срок истёк (скан) | `loan_id`, `reader_id`, `book_title`, `book_author`, `due_at`, `reader_email`, `reader_phone` |

Каждое сообщение декодируется из envelope, маппится в `domain.Notification` и
передаётся в сервис, который отправляет email через SMTP (или логирует SMS-заглушку).

## Запуск

```bash
cd notification-service
export NOTIFICATION_SERVICE_SMTP_HOST='smtp.example.com'
export NOTIFICATION_SERVICE_SMTP_USERNAME='user@example.com'
export NOTIFICATION_SERVICE_SMTP_PASSWORD='secret'
export NOTIFICATION_SERVICE_SMTP_FROM='notifications@example.com'
go run ./cmd/server
```

Переменные окружения:

| Переменная | По умолчанию | Назначение |
| --- | --- | --- |
| `NOTIFICATION_SERVICE_GRPC_ADDR` | `:8084` | адрес gRPC-сервера |
| `NOTIFICATION_SERVICE_HTTP_ADDR` | `:8094` | адрес REST/Swagger-сервера |
| `NOTIFICATION_SERVICE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `NOTIFICATION_SERVICE_LOG_FORMAT` | `json` | `json` или `text` |
| `NOTIFICATION_SERVICE_SHUTDOWN_TIMEOUT` | `15s` | таймаут graceful shutdown |
| `NOTIFICATION_SERVICE_SMTP_HOST` | *(обязательна)* | SMTP-сервер |
| `NOTIFICATION_SERVICE_SMTP_PORT` | `587` | порт SMTP |
| `NOTIFICATION_SERVICE_SMTP_USERNAME` | *(обязателен)* | логин SMTP |
| `NOTIFICATION_SERVICE_SMTP_PASSWORD` | *(обязателен)* | пароль SMTP |
| `NOTIFICATION_SERVICE_SMTP_FROM` | *(обязателен)* | адрес отправителя |
| `RABBITMQ_URL` | `amqp://guest:guest@localhost:5672/` | AMQP-строка подключения брокера |
| `RABBITMQ_EXCHANGE` | `library.events` | topic exchange доменных событий |

Liveness: **`GET /healthz`** → `200 OK`. Эндпоинт обслуживает HTTP-сервер
рядом с REST-слоем и используется healthcheck'ом контейнера.

## REST API и Swagger

HTTP-слой построен на grpc-gateway: маршруты берутся из аннотаций
`google.api.http` в `proto/notification/v1/notification.proto`, поэтому REST и
gRPC — это один и тот же контракт и одна и та же бизнес-логика. Запросы приходят
на `:8094` и проксируются в gRPC-сервер этого же процесса (`:8084`).

Документация: **http://localhost:8094/swagger/** (корень `/` редиректит туда же),
спецификация — http://localhost:8094/swagger/swagger.json. Ассеты Swagger UI
подключаются с CDN, поэтому для открытия страницы нужен доступ в интернет;
сама спека отдаётся из бинарника (`//go:embed`).

Все методы требуют заголовок `authorization: Bearer <token>` с токеном,
полученным из user-service (`Login`).

Liveness: **`GET /healthz`** → `200 OK`.

## Методы API

| Метод | Назначение |
| --- | --- |
| `Health` | healthcheck (возвращает пустой ответ) |

## Разработка

```bash
cd notification-service
go build ./...
go vet ./...
go test ./...
```

## Генерация кода

Контракт — `proto/notification/v1/notification.proto`. После его изменения:

```powershell
./scripts/gen_proto.ps1
```

## Ограничения текущей версии

- Email — единственный канал доставки (SMS заглушка, логируется).
- Нет очереди повторных попыток: если SMTP недоступен, сообщение отклоняется и
  попадает в dead-letter exchange.
- Нет REST-эндпоинтов для управления: сервис только потребляет события.
- API-gateway не маршрутизирует `/v1/notifications/*` на этот сервис (заглушка).

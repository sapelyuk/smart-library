# Smart Library

Полнофункциональная платформа управления библиотекой: **монорепозиторий** с
бэкендом на Go (gRPC-микросервисы), планируемым React-приложением и
AI-рекомендациями книг на LLM/RAG (RAG-прототип работает).

- **Бэкенд:** Go, gRPC + protobuf, grpc-gateway (REST/Swagger), PostgreSQL, RabbitMQ.
- **Фронтенд (в плане):** React SPA, потребляющая REST-контракты сервисов.
- **AI:** рекомендации книг через LLM/RAG как часть архитектуры, а не надстройка.
  RAG-прототип **работает от начала до конца** (n8n + pgvector + Gemini, 20 книг
  проиндексировано) — см. раздел «AI-first подход» и `services/ai-service/rag/README.md`.
  Go-адаптер сервиса (`ai.v1.AiService`) — не начат (issue #23).

Проект в активной разработке: бэкенд-сервисы и RAG-прототип работают, фронтенд и
Go-адаптер AI-сервиса — следующие этапы.

## Архитектура

| Сервис               | Ответственность                    | Порт |
|----------------------|------------------------------------|------|
| API Gateway          | Единая точка входа, маршрутизация  | 8080 |
| Book Service         | Каталог книг, ISBN, экземпляры     | 8081 |
| User Service         | Читатели, библиотекари, доступ     | 8082 |
| Loan Service         | Выдача/возврат, сроки возврата     | 8083 |
| Notification Service | Email/SMS-уведомления о сроках     | 8084 |
| AI Service           | Рекомендации книг через LLM/RAG    | 8085 |

Book Service дополнительно поднимает HTTP-слой для разработки на **8091**: REST-эндпоинты,
сгенерированные из аннотаций `google.api.http` его proto-контракта (grpc-gateway),
плюс Swagger UI по адресу `http://localhost:8091/swagger/`. User Service повторяет
это на **8082** (gRPC) / **8092** (REST + Swagger), Loan Service — на **8083**
(gRPC) / **8093** (REST + Swagger).

**API Gateway** — единая HTTP-точка входа для клиентских приложений. Клиент не знает
адресов отдельных микросервисов: все запросы идут на `:8080`, а шлюз маршрутизирует
их по префиксу пути:

| Префикс URL       | Сервис         | Порт HTTP (REST) |
|-------------------|----------------|------------------|
| `/v1/books/*`     | book-service   | 8091             |
| `/v1/borrow/*`    | book-service   | 8091             |
| `/v1/users/*`     | user-service   | 8092             |
| `/v1/auth/*`      | user-service   | 8092             |

Шлюз проверяет bearer-токен через gRPC `AuthenticateToken` user-service и
добавляет заголовок `X-Forwarded-For`. Локально запускается через
`docker compose up -d api-gateway`.

## Паттерны взаимодействия:
- Синхронно: gRPC между сервисами (быстро, типизированно)
- Асинхронно: **RabbitMQ** для событий (`loan.issued`, `loan.returned`,
  `loan.overdue`) —
  выбор зафиксирован в `docs/adr/0001-message-broker.md`: topic exchange
  `library.events`, routing key = тип события, publisher confirms + ручной ack,
  отложенные доставки через TTL + dead-letter exchange
- Обнаружение сервисов: DNS (Docker Compose локально, Kubernetes DNS в продакшене)
- Хранилище: отдельный PostgreSQL на каждый сервис (паттерн database-per-service)

## AI-first подход

В отличие от подхода «добавить AI в конец» (chatbot поверх готового продукта), в
Smart Library AI является **первоклассным компонентом архитектуры** с первого дня:

1. **Рекомендации — часть домена.** Каталог книг (Book Service) содержит
   метаданные, которые AI-сервис использует для формирования рекомендаций.
   Каждый сервис предоставляет gRPC-API — фронтенд и AI-модуль вызывают их
   независимо и параллельно.
2. **LLM/RAG через отдельный сервис.** AI-логика инкапсулирована в
   `services/ai-service/` (см. `docs/ai-first-principles.md` и ADR-0002
   `docs/adr/0002-ai-recommendation-architecture.md`):
   - **RAG для точности:** семантический поиск по каталогу книг через векторную
     БД (pgvector, контейнер `ai-rag-db` в `docker-compose.yml`), найденные
     фрагменты конкатенируются в контекст для LLM.
   - **Прототип работает от начала до конца** в `services/ai-service/rag/` —
     n8n workflow, схема pgvector, скрипты и документация. Проиндексировано
     20 книг, обе точки входа (chat UI и webhook) прошли smoke-тест.
   - **Промпты** в прототипе хранятся внутри n8n workflow; вынос в
     версионируемые файлы (`services/ai-service/prompts/`) — направление развития.
    - **Оценка качества** (`services/ai-service/rag/evals/`) — набор из 21
      тестового вопроса (taste/self/author/genre/constraint/refuse) с ожидаемыми
       книгами; скрипт `services/ai-service/rag/scripts/eval-rag.ps1` (метрики precision@k, recall@k,
       hit@k, MRR; пороги; отчёты); 53 offline-теста (`tests/`). Прогон:
       `powershell -File services/ai-service/rag/scripts/eval-rag.ps1 -ValidateOnly` (бесплатно, без сети).

   **Как устроен RAG-конвейер (работающий прототип):**

   - **Эмбеддинги:** Gemini `models/gemini-embedding-001`, 3072 измерения;
     в pgvector — `halfvec(3072)` (HNSW над `vector` ограничен 2000 измерениями,
     `halfvec` поднимает лимит до 4000).
   - **Поиск:** приближённый по косинусу, индекс **HNSW**
     (`book_chunks_embedding_idx`), функция `match_book_chunks`.
   - **Agentic RAG:** агент вызывает 4 инструмента — `Search Book Library`
     (векторный поиск), `Get Book Details` (точный lookup по `book_id`),
     `List Library` (просмотр каталога), `Remove Book` (удаление);
     chat-модель изолирована в одном узле (Gemini, документированная замена — Groq).
   - **Точки входа:** chat UI и `POST /webhook/book-rag/recommend`.
   - **Поток:** ingest → чанки → эмбеддинги → pgvector → retrieval → LLM → ответ.
   - Подробности: `services/ai-service/rag/README.md`,
     `rag/docs/ARCHITECTURE.md`, `rag/docs/USAGE.md`.
3. **Фронтенд потребляет AI-контент как данные.** React-приложение получает
   рекомендации через REST-эндпоинт AI-сервиса и рендерит их в том же UI, что
   и обычные данные из каталога.
4. **Безопасность.** AI-модуль не имеет прямого доступа к БД сервисов — все
   данные поступают через gRPC-API с авторизацией. Промпты не содержат
   пользовательских данных без явного согласия.

Полные принципы описаны в `docs/ai-first-principles.md`.

## Состояние (на 2026-10)

| Компонент                                             | Состояние                                              |
|-------------------------------------------------------|--------------------------------------------------------|
| Общий `pkg/` (logger, config, migrate)                | готово                                                 |
| Book Service (proto, domain, service, repository, handler, server) | готово, хранилище **PostgreSQL** (PG 17 + `pgx/v5`) |
| Book Service REST + Swagger UI (grpc-gateway, `:8091`) | готово                                                |
| Book Service PostgreSQL repository                     | готово — `#9` (PG 17 + `pgx/v5`): миграции при старте, `FOR UPDATE SKIP LOCKED`, тесты против PG |
| User Service (proto, domain, security, service, handler, server) | готово, хранилище **PostgreSQL** (argon2id + bearer-токены) |
| User Service REST + Swagger UI (grpc-gateway, `:8092`) | готово                                                |
| Миграции User Service (`pkg/migrate`, embed FS)        | готовы, применяются при старте                         |
| Локальный запуск сервисов                              | `cd services/api-gateway` (HTTP `:8080`) / `cd services/book-service` (gRPC `:8081`, REST `:8091`) / `cd services/user-service` (gRPC `:8082`, REST `:8092`) / `cd services/loan-service` (gRPC `:8083`, REST `:8093`) |
| Контейнеризация (multi-stage Dockerfile + compose)    | готово — `#6` (PR #39); сервисы + 4 БД + RabbitMQ в `docker-compose.yml`, healthcheck `GET /healthz` |
| CI (GitHub Actions: build + test + race + coverage)   | готово — `#5` (PR #28); тесты book- и loan-service идут против сервис-контейнера `postgres:17` |
| OpenAPI/Swagger из proto-аннотаций                    | готово: `grpc-gateway` генерирует REST-маршруты и `swagger.json` (embed в сервисы) |
| Брокер сообщений: выбор и локальная инфраструктура    | готово: ADR-0001 (RabbitMQ), `docker-compose.yml`; реализация продюсеров/консьюмеров — `#14` |
| Обнаружение сервисов: выбор механизма                  | готово: ADR-0003 (Kubernetes DNS + Docker Compose DNS); реализация — `#8` |
| AI Service: RAG-прототип (n8n + pgvector + Gemini)    | **работает end-to-end**: 20 книг проиндексировано, chat UI и webhook прошли smoke-тест; оценка качества: eval-набор (21 вопрос) + `scripts/eval-rag.ps1` + 53 offline-теста (`evals/` + `tests/`); артефакты в `services/ai-service/rag/` |
| AI Service: Go-адаптер `ai.v1.AiService`              | не начато — `#23`; архитектура — ADR-0002              |
| Notification Service                                   | не начато — `#48`                                      |
| API Gateway                                            | готово — `#10` (gRPC-аутентификация через user-service, маршрутизация по пути, Dockerfile) |
| Межсервисные gRPC-клиенты, discovery                  | loan-service вызывает book-service (`BorrowCopy`/`ReturnCopy`) и user-service (`AuthenticateToken`); gateway — user-service; discovery — `#8` |
| User Service: техдолг (хранилище и API)                | `#32` (pgx вместо lib/pq), `#33` (pg_trgm + keyset-пагинация), `#34` (rate limiting), `#31` (кэш валидации сессий) |

Локальный запуск Book Service (нужна база PostgreSQL — поднимается через
`docker compose up -d book-db`; `BOOK_SERVICE_DB_DSN` обязателен, миграции
применяются при старте):

```bash
cd services/book-service
export BOOK_SERVICE_DB_DSN='host=localhost port=5434 user=library password=library dbname=library_books sslmode=disable'
go run ./cmd/server     # gRPC на :8081, REST + Swagger на :8091
```

Локальный запуск User Service (нужна база PostgreSQL — поднимается через
`docker compose up -d user-db`; переменные сида `USER_SERVICE_SEED_LIBRARIAN_*`
описаны в `services/user-service/README.md`):

```bash
cd services/user-service
go run ./cmd/server     # gRPC на :8082, REST + Swagger на :8092
```

Локальный запуск Loan Service (нужна база PostgreSQL — поднимается через
`docker compose up -d loan-db`, и запущенные book- и user-service; переменные
`LOAN_SERVICE_*` описаны в `services/loan-service/README.md`):

```bash
cd services/loan-service
export LOAN_SERVICE_DB_DSN='host=localhost port=5435 user=library password=library dbname=library_loans sslmode=disable'
export LOAN_SERVICE_BOOK_SERVICE_GRPC_ADDR='localhost:8081'
export LOAN_SERVICE_USER_SERVICE_GRPC_ADDR='localhost:8082'
go run ./cmd/server     # gRPC на :8083, REST + Swagger на :8093
```

Сборка / проверка / тесты (из директории модуля, не из корня репозитория):

```bash
go build ./... && go vet ./... && go test ./...
```

Перегенерация кода gRPC после правки `.proto`-контракта:

```powershell
./scripts/gen_proto.ps1
```

Полный стек одной командой (сборка образов сервисов + инфраструктура):

```bash
docker compose up -d --build    # сервисы :8080/:8081/:8091/:8082/:8092/:8083/:8093 + БД + RabbitMQ
docker compose logs -f book-service
docker compose down -v          # остановить и удалить volume
```

Состав `docker-compose.yml`:

| Сервис (compose-ключ) | Образ | Порты хоста |
| --- | --- | --- |
| `api-gateway` | build `services/api-gateway/Dockerfile` | `8080` (REST-маршрутизация, gRPC-аутентификация, `/healthz`) |
| `book-service` | build `services/book-service/Dockerfile` | `8081` (gRPC), `8091` (REST + Swagger + `/healthz`) |
| `user-service` | build `services/user-service/Dockerfile` | `8082` (gRPC), `8092` (REST + Swagger + `/healthz`) |
| `loan-service` | build `services/loan-service/Dockerfile` | `8083` (gRPC), `8093` (REST + Swagger + `/healthz`) |
| `book-db` | `postgres:17` | `5434` |
| `user-db` | `postgres:17` | `5432` |
| `loan-db` | `postgres:17` | `5435` |
| `ai-rag-db` | `pgvector/pgvector:pg17` | `5433` |
| `rabbitmq` | `rabbitmq:4-management` | `5672`, `15672` |

Контейнеры создаются с префиксом `library-` (через `container_name` в `docker-compose.yml`): `library-api-gateway`, `library-book-service`, `library-user-service`, `library-loan-service`, `library-book-db`, `library-user-db`, `library-loan-db`, `library-ai-rag-db`, `library-rabbitmq`.

Все Dockerfile'ы — multi-stage (`golang:1.27-alpine` → `alpine:3.21`), собираются из **корня репозитория**:
модули используют `replace ... => ../../pkg`, поэтому контекст сборки — корень, а не каталог сервиса.
`loan-service` дополнительно заменяет `book-service` и `user-service` (импортирует их сгенерированные
proto-пакеты), поэтому его Dockerfile копирует исходники обоих sibling-модулей.
Контейнеры работают под непривилегированным пользователем; healthcheck бьёт в `GET /healthz`
(HTTP-эндпоинт, добавленный рядом с REST-слоем).

### Переменные окружения API Gateway

| Переменная | Назначение | По умолчанию |
| --- | --- | --- |
| `GATEWAY_HTTP_ADDR` | Адрес HTTP-сервера шлюза | `:8080` |
| `GATEWAY_BOOK_SERVICE_URL` | REST-адрес book-service (обязательная) | — |
| `GATEWAY_USER_SERVICE_URL` | REST-адрес user-service (обязательная) | — |
| `GATEWAY_USER_SERVICE_GRPC_ADDR` | gRPC-адрес user-service для AuthenticateToken (обязательная) | — |
| `GATEWAY_LOG_LEVEL` | Уровень логирования: debug\|info\|warn\|error | `info` |
| `GATEWAY_LOG_FORMAT` | Формат логов: json\|text | `json` |
| `GATEWAY_SHUTDOWN_TIMEOUT` | Бюджет graceful shutdown | `15s` |


Учётные данные брокера берутся из `RABBITMQ_USER`/`RABBITMQ_PASS` (по умолчанию `guest`).
Векторная БД `ai-rag-db` (pgvector) настраивается через `AI_RAG_DB_USER`/`AI_RAG_DB_PASSWORD`/`AI_RAG_DB_NAME`
(по умолчанию `bookrag`), схема применяется из `services/ai-service/rag/db/`. Быстрый старт RAG-прототипа —
в `services/ai-service/rag/README.md`.

## Структура проекта (фактическая)

```
smart-library/
├── go.work              # воркспейс: ./services/api-gateway, ./services/book-service, ./services/user-service, ./services/loan-service, ./pkg
├── README.md            # этот файл
├── KODA.md              # контекст репозитория для AI-сессий
├── docker-compose.yml   # сервисы + БД + RabbitMQ: api-gateway (:8080), book/user/loan-service, book-db (:5434), user-db (:5432), loan-db (:5435), pgvector (:5433)
├── .dockerignore        # исключает .git, go.work, артефакты из контекста сборки
├── .github/workflows/   # CI: build + test (ci.yml)
├── docs/
│   ├── adr/
│   │   ├── 0001-message-broker.md  # решение по брокеру сообщений
│   │   ├── 0002-ai-recommendation-architecture.md  # архитектура AI-сервиса
│   │   └── 0003-service-discovery.md  # механизм обнаружения сервисов
│   └── ai-first-principles.md  # принципы AI-first подхода
├── scripts/
│   └── gen_proto.ps1    # кодогенерация protoc + go/go-grpc/grpc-gateway/openapiv2
├── third_party/         # vendored .proto includes (google/api, openapiv2 options)
├── tools/
│   └── protoc/          # локальный protoc 36.2
├── internal/            # артефакт до реструктуризации #19 (не импортируется; кандидат на удаление)
├── pkg/                 # общие библиотеки (config, logger, migrate)
└── services/
    ├── api-gateway/     # реализован (#10): gRPC-аутентификация через user-service, маршрутизация запросов к book/user сервисам, порт :8080
    ├── book-service/    # реализован, см. services/book-service/README.md
    │   ├── Dockerfile   # multi-stage образ (контекст сборки — корень репозитория)
    │   ├── cmd/server/
    │   ├── proto/book/v1/
    │   ├── gen/go/      # генерация, не править руками
    │   ├── docs/        # swagger.json (генерация) + обёртка go:embed
    │   ├── internal/    # domain, repository (postgres), testdb, service, handler
    │   └── migrations/
    ├── user-service/    # реализован, см. services/user-service/README.md
    │   ├── Dockerfile   # multi-stage образ (контекст сборки — корень репозитория)
    │   ├── cmd/server/
    │   ├── proto/user/v1/
    │   ├── gen/go/      # генерация, не править руками
    │   ├── docs/        # swagger.json (генерация) + обёртка go:embed
    │   ├── internal/    # domain, security, repository (postgres), service, handler
    │   └── migrations/
    ├── loan-service/    # реализован (#49), см. services/loan-service/README.md
    │   ├── Dockerfile   # multi-stage образ (контекст сборки — корень репозитория)
    │   ├── cmd/server/
    │   ├── proto/loan/v1/
    │   ├── gen/go/      # генерация, не править руками
    │   ├── docs/        # swagger.json (генерация) + обёртка go:embed
    │   ├── internal/    # domain, service, repository (postgres), bookclient, usersvc, auth, events, testdb, handler
    │   └── migrations/
    └── ai-service/      # ADR-0002; RAG-прототип работает, см. services/ai-service/README.md
        └── rag/         # рабочий прототип RAG: n8n workflow, pgvector схема (db/),
                         # скрипты (scripts/), документация (docs/),
                         # evals/ — 21 вопрос с ожидаемыми книгами,
                         # tests/ — 53 offline-теста, .env.example — шаблон переменных
```

Сервис `notification-service` из архитектурной таблицы ещё не создан как
директория — заведена задача `#48`.

Для локальной разработки нескольких модулей одновременно используется Go-воркспейс
(`go.work`).

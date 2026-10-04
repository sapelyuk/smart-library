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
это на **8082** (gRPC) / **8092** (REST + Swagger).

## Паттерны взаимодействия:
- Синхронно: gRPC между сервисами (быстро, типизированно)
- Асинхронно: **RabbitMQ** для событий (`book.borrowed`, `loan.overdue`) —
  выбор зафиксирован в `docs/adr/0001-message-broker.md`: topic exchange
  `library.events`, routing key = тип события, publisher confirms + ручной ack,
  отложенные доставки через TTL + dead-letter exchange
- Обнаружение сервисов: Consul или Kubernetes DNS
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
| Book Service (proto, domain, service, repository, handler, server) | готово, хранилище in-memory         |
| Book Service REST + Swagger UI (grpc-gateway, `:8091`) | готово                                                |
| Book Service PostgreSQL repository                     | в работе — `#9` (PG 17 + `pgx/v5`); миграция `001_init.sql` готова |
| User Service (proto, domain, security, service, handler, server) | готово, хранилище **PostgreSQL** (argon2id + bearer-токены) |
| User Service REST + Swagger UI (grpc-gateway, `:8092`) | готово                                                |
| Миграции User Service (`pkg/migrate`, embed FS)        | готовы, применяются при старте                         |
| Локальный запуск сервисов                              | `cd services/book-service` (gRPC `:8081`, REST `:8091`) / `cd services/user-service` (gRPC `:8082`, REST `:8092`) |
| Контейнеризация (multi-stage Dockerfile + compose)    | готово — `#6` (PR #39); сервисы + 3 БД + RabbitMQ в `docker-compose.yml`, healthcheck `GET /healthz` |
| CI (GitHub Actions: build + test + race + coverage)   | готово — `#5` (PR #28)                                |
| OpenAPI/Swagger из proto-аннотаций                    | готово: `grpc-gateway` генерирует REST-маршруты и `swagger.json` (embed в сервисы) |
| Брокер сообщений: выбор и локальная инфраструктура    | готово: ADR-0001 (RabbitMQ), `docker-compose.yml`; реализация продюсеров/консьюмеров — `#14` |
| AI Service: RAG-прототип (n8n + pgvector + Gemini)    | **работает end-to-end**: 20 книг проиндексировано, chat UI и webhook прошли smoke-тест; оценка качества: eval-набор (21 вопрос) + `scripts/eval-rag.ps1` + 53 offline-теста (`evals/` + `tests/`); артефакты в `services/ai-service/rag/` |
| AI Service: Go-адаптер `ai.v1.AiService`              | не начато — `#23`; архитектура — ADR-0002              |
| Loan / Notification Service                            | не начато (задачи не заведены)                         |
| API Gateway                                            | не начато — `#10`                                     |
| Межсервисные gRPC-клиенты, discovery                  | не начато; User Service отдаёт `AuthenticateToken` для gateway; discovery — `#8` |

Локальный запуск Book Service (хранилище in-memory, внешние зависимости не нужны):

```bash
cd services/book-service
go run ./cmd/server     # gRPC на :8081, REST + Swagger на :8091
```

Локальный запуск User Service (нужна база PostgreSQL — поднимается через
`docker compose up -d user-db`; переменные сида `USER_SERVICE_SEED_LIBRARIAN_*`
описаны в `services/user-service/README.md`):

```bash
cd services/user-service
go run ./cmd/server     # gRPC на :8082, REST + Swagger на :8092
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
docker compose up -d --build    # сервисы :8081/:8091 и :8082/:8092 + БД + RabbitMQ
docker compose logs -f book-service
docker compose down -v          # остановить и удалить volume
```

Состав `docker-compose.yml`:

| Контейнер | Образ | Порты хоста |
| --- | --- | --- |
| `book-service` | build `services/book-service/Dockerfile` | `8081` (gRPC), `8091` (REST + Swagger + `/healthz`) |
| `user-service` | build `services/user-service/Dockerfile` | `8082` (gRPC), `8092` (REST + Swagger + `/healthz`) |
| `book-db` | `postgres:17` | `5434` |
| `user-db` | `postgres:17` | `5432` |
| `ai-rag-db` | `pgvector/pgvector:pg17` | `5433` |
| `rabbitmq` | `rabbitmq:4-management` | `5672`, `15672` |

Оба Dockerfile'а — multi-stage (`golang:1.27-alpine` → `alpine:3.21`), собираются из **корня репозитория**:
модули используют `replace ... => ../../pkg`, поэтому контекст сборки — корень, а не каталог сервиса.
Контейнеры работают под непривилегированным пользователем; healthcheck бьёт в `GET /healthz`
(HTTP-эндпоинт, добавленный рядом с REST-слоем).


Учётные данные брокера берутся из `RABBITMQ_USER`/`RABBITMQ_PASS` (по умолчанию `guest`).
Векторная БД `ai-rag-db` (pgvector) настраивается через `AI_RAG_DB_USER`/`AI_RAG_DB_PASSWORD`/`AI_RAG_DB_NAME`
(по умолчанию `bookrag`), схема применяется из `services/ai-service/rag/db/`. Быстрый старт RAG-прототипа —
в `services/ai-service/rag/README.md`.

## Структура проекта (фактическая)

```
smart-library/
├── go.work              # воркспейс: ./services/book-service, ./services/user-service, ./pkg
├── README.md            # этот файл
├── KODA.md              # контекст репозитория для AI-сессий
├── docker-compose.yml   # сервисы + БД + RabbitMQ: book/user-service, book-db (:5434), user-db (:5432), pgvector (:5433)
├── .dockerignore        # исключает .git, go.work, артефакты из контекста сборки
├── .github/workflows/   # CI: build + test (ci.yml)
├── docs/
│   ├── adr/
│   │   ├── 0001-message-broker.md  # решение по брокеру сообщений
│   │   └── 0002-ai-recommendation-architecture.md  # архитектура AI-сервиса
│   └── ai-first-principles.md  # принципы AI-first подхода
├── scripts/
│   ├── gen_proto.ps1    # кодогенерация protoc + go/go-grpc/grpc-gateway/openapiv2
│   └── eval-rag.ps1     # прогон eval-набора RAG (precision@k, recall@k, hit@k, MRR)
├── third_party/         # vendored .proto includes (google/api, openapiv2 options)
├── tools/
│   └── protoc/          # локальный protoc 36.2
├── pkg/                 # общие библиотеки (config, logger, migrate)
└── services/
    ├── book-service/    # реализован, см. services/book-service/README.md
    │   ├── Dockerfile   # multi-stage образ (контекст сборки — корень репозитория)
    │   ├── cmd/server/
    │   ├── proto/book/v1/
    │   ├── gen/go/      # генерация, не править руками
    │   ├── docs/        # swagger.json (генерация) + обёртка go:embed
    │   ├── internal/    # domain, repository (in-memory), service, handler
    │   └── migrations/
    ├── user-service/    # реализован, см. services/user-service/README.md
    │   ├── Dockerfile   # multi-stage образ (контекст сборки — корень репозитория)
    │   ├── cmd/server/
    │   ├── proto/user/v1/
    │   ├── gen/go/      # генерация, не править руками
    │   ├── docs/        # swagger.json (генерация) + обёртка go:embed
    │   ├── internal/    # domain, security, repository (postgres), service, handler
    │   └── migrations/
    └── ai-service/      # ADR-0002; RAG-прототип работает, см. services/ai-service/README.md
        └── rag/         # рабочий прототип RAG: n8n workflow, pgvector схема (db/),
                         # скрипты (scripts/), документация (docs/),
                         # evals/ — 21 вопрос с ожидаемыми книгами,
                         # tests/ — 53 offline-теста, .env.example — шаблон переменных
```

Сервисы `api-gateway`, `loan-service` и `notification-service` из архитектурной
таблицы ещё не созданы как директории.

Для локальной разработки нескольких модулей одновременно используется Go-воркспейс
(`go.work`).

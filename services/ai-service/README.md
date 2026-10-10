# AI Service

Сервис рекомендаций книг через LLM/RAG. Архитектурное решение —
ADR-0002 (`docs/adr/0002-ai-recommendation-architecture.md`).

## Статус

| Часть | Состояние |
| --- | --- |
| Архитектура (ADR-0002) | готово |
| RAG-прототип (n8n + pgvector + Gemini) | **работает end-to-end**: 20 книг проиндексировано, chat UI и webhook прошли smoke-тест |
| Артефакты прототипа RAG (`rag/`) | перенесены (задача #22) |
| Go-модуль: `ai.v1.AiService` (proto, gRPC/REST, n8n-клиент) | **готово** (задача #23) |
| Хранилище pgvector (`pgx/v5`), миграции, конструктор индекса | **готово** (задача #23) |
| Индексация каталога (полная синхронизация, отдельный режим) | **готово** (задача #23) |
| Синхронизация каталога через события (`book.*`) | **готово** (задача #23): консьюмер RabbitMQ |

## Контракт

`ai.v1.AiService` (proto + grpc-gateway, порты 8085 gRPC / 8095 REST).
Каждый метод требует заголовок `authorization: Bearer <токен>`; методы
индекса additionally требуют роль `librarian`:

| RPC | REST | Доступ |
| --- | --- | --- |
| `Recommend` | `POST /v1/ai/recommendations` | любой аутентифицированный |
| `IngestBook` | `POST /v1/ai/index/books/{book_id}` | `librarian` |
| `DeleteBook` | `DELETE /v1/ai/index/books/{book_id}` | `librarian` |
| `IndexStatus` | `GET /v1/ai/index/status` | `librarian` |

Swagger UI — на `http://localhost:8095/swagger/`.

Ответы вне RAG не порождаются: если воркфлоу (n8n) недоступен, выключен или
исчерпал квоту провайдера, сервис отвечает `UNAVAILABLE`
(`domain.ErrBackendUnavailable`), а не генерирует текст сам. Фоллбэка на
генерацию нет по определению (ADR-0002). Ошибки домена отображаются на коды
gRPC в `internal/handler/grpc.go`: `InvalidArgument` (пустой/длинный запрос,
некорректный id), `Unauthenticated`, `PermissionDenied`, `NotFound` (книги
нет в каталоге), `Unavailable` (бэкенд или индекс недоступны).

## Переменные окружения

Префикс `AI_` (см. `internal/config/config.go`). Обязательные:
`AI_CATALOG_GRPC_ADDR`, `AI_USER_GRPC_ADDR`, `AI_N8N_BASE_URL`,
`AI_N8N_HEADER_VALUE`, `AI_RAG_STORE_DSN`. Без них сервис не стартует.

Запуск локально (нужны `book-service`, `user-service`, `ai-rag-db`, n8n):

```bash
docker compose up -d rabbitmq user-db book-db ai-rag-db user-service book-service
cd services/ai-service
export AI_CATALOG_GRPC_ADDR=localhost:8081
export AI_USER_GRPC_ADDR=localhost:8082
export AI_RAG_STORE_DSN='host=localhost port=5433 user=bookrag password=bookrag dbname=bookrag sslmode=disable'
export AI_N8N_BASE_URL=http://localhost:5678
export AI_N8N_HEADER_VALUE=<ключ из rag/.env>
go run ./cmd/server        # gRPC на :8085, REST + Swagger на :8095
```

Флагов однократной полной переиндексации нет: индекс наполняет консьюмер
событий `book.*` (`AI_CONSUMER_ENABLED=false` его отключает), а отдельно взятую
книгу переуказывает `IngestBook`.


## Каталог `rag/`

**Работающий** прототип agentic RAG-системы (n8n workflow + pgvector + Gemini):

- `rag/README.md` — назначение, состав, быстрый старт, переменные окружения.
- `rag/docs/` — документация прототипа: `ARCHITECTURE.md`, `SETUP.md`, `USAGE.md`.
- `rag/db/01-schema.sql` — схема pgvector (расширение, таблицы, функции поиска).
- `rag/workflow/` — экспорт n8n workflow.
- `rag/scripts/` — скрипты развёртывания и проверки (start/verify/import/ingest).

Ключевые параметры RAG-конвейера: эмбеддинги Gemini `models/gemini-embedding-001`
(3072 dims), вектор `halfvec(3072)`, HNSW-индекс, функция `match_book_chunks`;
агент использует 4 инструмента (векторный поиск, точный lookup, просмотр каталога,
удаление). Точки входа — chat UI и `POST /webhook/book-rag/recommend`.

Векторное хранилище поднято в корневом `docker-compose.yml` (контейнер
`ai-rag-db`, порт `:5433`, init-скрипты монтируются из `rag/db/`):

```bash
docker compose up -d ai-rag-db   # только pgvector
```

Схема применима и к отдельному стеку `rag/docker-compose.yml` (для работы
с прототипом без монорепо-инфраструктуры).

# n8n Book-Recommender RAG System

Самостоятельно размещаемая **agentic RAG**-система, построенная как workflow n8n, которая индексирует
библиотеку книг и рекомендует произведения, которые пользователю, вероятно, понравятся.

Две точки входа, один агент:

| Точка входа | Узел | Назначение |
|---|---|---|
| Chat UI | `When chat message received` (chat trigger) | Интерактивный чат во встроенной панели чата n8n |
| HTTP API | `Recommend Webhook` — POST `/webhook/book-rag/recommend` | Программный доступ из приложений/ботов |

Два пути загрузки, **одна** цепочка индексации:

| Путь | Узел | Назначение |
|---|---|---|
| Массовый | `Index Book Library` (manual trigger) | Индексация файла каталога, например `samples/books.csv` |
| Одиночный | `Ingest Webhook` — POST `/webhook/book-rag/ingest` | Добавление или обновление одной книги из приложения |

> **Статус: работает от начала до конца.** База данных проверена, созданы 3 учётных данных (credential),
> workflow активен, **проиндексировано 20 книг**, и агент отвечает рекомендациями, основанными на реальных
> строках каталога (обе точки входа прошли дымовой тест (smoke test)). **Добавлено, но не делалось:** набор
> из 30 оценочных вопросов `evals/rag-eval-suite.json` и скрипт `scripts\eval-rag.ps1` — запустите их,
> прежде чем утверждать, что качество поиска улучшилось. См. [docs/PROGRESS.md](docs/PROGRESS.md) Сессия 4b
> для доказательств и запасного варианта с Groq.

## Карта документации

Читайте их по порядку, когда возобновляете работу в новой сессии:

1. **[docs/RULES.md](docs/RULES.md)** — жёсткие ограничения и соглашения. Читать первым, никогда не нарушать.
2. **[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)** — модель данных, граф узлов, поток данных.
3. **[docs/DECISIONS.md](docs/DECISIONS.md)** — почему стек именно такой (журнал ADR).
4. **[docs/PROGRESS.md](docs/PROGRESS.md)** — что сделано, что дальше, известные блокеры.
5. **[docs/SETUP.md](docs/SETUP.md)** — пошаговый запуск.
6. **[docs/USAGE.md](docs/USAGE.md)** — как этим реально пользоваться, с примерами промптов и вызовов API.

## Стек (выбран и проверен)

- **n8n** самостоятельно размещённый, глобальная установка через npm, версия 2.40.7, каталог данных `C:\Users\ThinkPro\.n8n`
- **Postgres + pgvector 0.8.7** в Docker, контейнер `n8n-book-rag-db`, порт хоста `5433`
- **Эмбеддинги:** Google Gemini, `models/gemini-embedding-001` → **3072 dims**, `halfvec(3072)` (измерено
  на живом API 2026-10-02)
- **Чат-модель агента:** как собрано, `models/gemini-3-flash-preview` на узле Gemini `Chat Model (swap me)`
  — намеренно изолирована в одном узле. Сейчас она делит ключ Gemini, а значит и квоту, с эмбеддингами;
  **Groq — это документированная альтернатива на бесплатном тарифе**, и он снова доступен из этой сети.
  См. ADR-005a.
- **Доказано, а не предположение:** схема была применена к живому контейнеру (`ON_ERROR_STOP=1`, код выхода 0), все
  четыре функции были проверены на реальных строках, `EXPLAIN` подтверждает, что используется индекс HNSW,
  workflow импортируется и загружается в n8n без ошибок, и обе конечные точки моделей отвечают **HTTP 200** для ключей
  в `.env`. Слой LLM/агента теперь **выполнен от начала до конца** — проиндексировано 20 книг, обе точки входа
  прошли дымовой тест (smoke test), все четыре инструмента агента проверены.

## Статус: что работает и что вы должны сделать

**Работает сейчас:** всё. База данных + 4 функции, 3 учётных данных (credential) привязаны к 12 узлам, workflow активен,
проиндексировано 20 книг, и агент рекомендует по реальным строкам.

**Осталось решение на ваше усмотрение, а не шаг сборки:** бесплатный тариф Gemini мал, и чат с
эмбеддингами делят его (см. Сессию 4b). Если агент начнёт возвращать `429`, переключите чат-модель на Groq —
[docs/SETUP.md](docs/SETUP.md) шаг 3 объясняет точно как, и это изменение одного узла.

```powershell
# 1. Bring the database up (already running; safe to re-run)
powershell -ExecutionPolicy Bypass -File .\scripts\start-db.ps1

# 2. Verify the schema is intact
powershell -ExecutionPolicy Bypass -File .\scripts\verify-db.ps1

# 3. Start n8n, then open http://localhost:5678
& "$env:APPDATA\npm\n8n.cmd" start

# 4. Load the catalogue (idempotent; the workflow's own bulk trigger is broken - see SETUP.md step 7)
powershell -ExecutionPolicy Bypass -File .\scripts\ingest-catalogue.ps1
```

> **Почему `-ExecutionPolicy Bypass`:** ваша машина по умолчанию блокирует файлы `.ps1`. Скрипты — это обычный
> PowerShell — сначала прочитайте их, если предпочитаете.

Затем следуйте шагам 3–8 из [docs/SETUP.md](docs/SETUP.md):
1. ваш ключ Gemini покрывает **и** эмбеддинги, **и** чат-модель агента — второй провайдер не нужен
2. создайте **3 учётных данных (credential)**, названные точно так, как они перечислены в шаге 5 SETUP.md (workflow связывает их по имени)
3. привяжите их к 12 узлам из шага 6 SETUP.md
4. проиндексируйте каталог с помощью `scripts\ingest-catalogue.ps1`, затем запустите дымовые тесты (smoke test) чата и webhook
5. см. [docs/USAGE.md](docs/USAGE.md) для готовых вызовов API, полей payload и диагностики проблем

## Структура репозитория

```
n8n-rag-system/
  README.md                    this file
  docker-compose.yml           pgvector container (port 5433, isolated)
  .env                         real local config - GIT-IGNORED, holds the DB password
  .env.example                 template to copy from
  db/
    01-schema.sql              tables, indexes, 4 functions - IDEMPOTENT, applied with ON_ERROR_STOP
    99-smoke-test.sql          15-step proof that the schema works (run this after changes)
    99-assertions.sql          exact expected-value checks for the read functions
  workflow/
    book-rag-system.json       the importable n8n workflow (28 nodes, id BookRagSys2026)
  samples/
    books.csv                  20 real books with descriptions, across 7 genres
  scripts/
    start-db.ps1               compose up + wait for healthy
    stop-db.ps1                compose down (keeps data; -RemoveVolume is guarded)
    verify-db.ps1              connectivity + extension + column type + index + functions
    check-embedding-dim.ps1    asks the live API how wide its embeddings are
    ingest-catalogue.ps1       bulk-load a catalogue via the ingest webhook (idempotent)
    import-workflow.ps1        n8n CLI import helper (USE -Update to re-import)
    validate_workflow.py       structural checks on the workflow JSON - run after every edit
    eval-rag.ps1               quality gates: runs the eval suite, reports precision@k / recall@k / hit@k / MRR
    eval_metrics.py            re-scores a saved run, audits the suite - offline, no API key
  evals/
    README.md                  what the metrics mean and how to add a question
    rag-eval-suite.json        the eval set: 30 questions -> expected books
    fixtures/                  synthetic runs used by the offline tests
    results/                   generated reports (git-ignored)
  tests/
    test_eval_metrics.py       53 offline tests for the metric + matching rules
    _make_fixtures.py          regenerates evals/fixtures/
  docs/
    RULES.md  ARCHITECTURE.md  DECISIONS.md  PROGRESS.md  SETUP.md  USAGE.md
```

## Запускайте это после изменений

```powershell
# after editing the workflow JSON
python scripts\validate_workflow.py

# structural checks do NOT catch data-flow bugs - 6 of them survived a clean validation run.
# After a workflow change, always re-run one ingest and one recommendation end to end:
powershell -ExecutionPolicy Bypass -File .\scripts\ingest-catalogue.ps1 -Path .\samples\books.csv

# after editing the schema
docker cp db/01-schema.sql n8n-book-rag-db:/tmp/s.sql
docker exec n8n-book-rag-db psql -U bookrag -d bookrag -v ON_ERROR_STOP=1 -f /tmp/s.sql
docker cp db/99-smoke-test.sql n8n-book-rag-db:/tmp/t.sql
docker exec n8n-book-rag-db psql -U bookrag -d bookrag -v ON_ERROR_STOP=1 -f /tmp/t.sql
```

Затем следуйте [docs/SETUP.md](docs/SETUP.md) для стороны n8n.

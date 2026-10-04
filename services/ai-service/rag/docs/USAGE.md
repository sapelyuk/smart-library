# USAGE — как на самом деле пользоваться системой Book RAG

> Каждый payload, имя поля и форма ответа ниже были прочитаны из
> `workflow/book-rag-system.json` 2026-10-02, а не выдуманы. Если вы меняете узел, меняйте и этот файл
> (RULES §4.3).

---

## 0. Прежде чем что-либо заработает

Два предварительных условия, оба легко забыть:

1. **Учётные данные (credential) привязаны.** Все 12 привязок учётных данных (credential) должны быть заполнены — см.
   [SETUP.md](SETUP.md), шаг 6. Узел с красным треугольником предупреждения провалит запуск.
2. **Библиотека должна быть проиндексирована.** Агент читает `book_chunks`, и прямо сейчас эта таблица **пуста**
   (`books 0, book_chunks 0`). Пока вы не выполните массовую загрузку из §2, агент будет корректно сообщать, что
   ему нечего рекомендовать.

**Workflow должен быть Active, чтобы production-URL webhook отвечали.** Этим управляют переключатели в правом верхнем
углу холста. Панель чата работает, пока вы в редакторе, и без этого.

---

## 1. Две точки входа

| Точка входа | Где | Аутентификация |
|---|---|---|
| **Chat** | Кнопка **Chat** в левом нижнем углу холста workflow | Вход в n8n (для chat-триггера задано `public: false`) |
| **HTTP API** | `POST http://localhost:5678/webhook/book-rag/recommend` | Заголовок `x-book-rag-key` |

Обе подают данные одному и тому же агенту через `Normalise Chat Input`, поэтому поведение и ответы идентичны.

### Chat: запросы, которые работают хорошо

Агент проинструктирован искать по *вкусу*, а не по точному названию, поэтому описывайте, что вы хотите:

- `I loved Dune — recommend something similar.`
- `Something short and funny, nothing bleak.`
- `A mystery with a strong female lead, under 400 pages.`
- `I want dense political worldbuilding like Le Guin.`
- `What do you have in Cyberpunk?` — вопрос по каталогу; использует `List Library`
- `Tell me about Neuromancer.` — вопрос о деталях; использует `Get Book Details`

У агента **четыре инструмента**: `Search Book Library` (семантический поиск, всегда пробуется первым),
`Get Book Details`, `List Library` и `Remove Book`. Ему явно запрещено выдумывать названия —
если он называет книгу, эта книга есть в вашей библиотеке.

Задайте уточняющий вопрос (`something shorter?`), чтобы убедиться, что `Postgres Chat Memory` работает; он хранит последние
10 сообщений на каждый `sessionId`.

### HTTP API: recommend

```powershell
$headers = @{ 'x-book-rag-key' = '<WEBHOOK_HEADER_VALUE from .env>' }
$body = @{
  chatInput = 'Recommend a mystery novel with a strong female lead'
  sessionId = 'test-1'          # optional; reuse it to keep conversation memory
} | ConvertTo-Json

Invoke-RestMethod -Method Post `
  -Uri 'http://localhost:5678/webhook/book-rag/recommend' `
  -Headers $headers -ContentType 'application/json' -Body $body
```

Форма ответа (из `Respond to Recommend`):

```json
{ "output": "…the agent's recommendation text…", "sessionId": "test-1" }
```

`sessionId` возвращается обратно из запроса. Отправьте **тот же** `sessionId` в следующем вызове, чтобы продолжить
тот же разговор; отправьте новый, чтобы начать заново.

Тот же вызов через `curl`:

```bash
curl -s -X POST http://localhost:5678/webhook/book-rag/recommend \
  -H "x-book-rag-key: <WEBHOOK_HEADER_VALUE>" \
  -H "Content-Type: application/json" \
  -d '{"chatInput":"something short and funny","sessionId":"test-1"}'
```

---

## 2. Массовая загрузка — весь каталог

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\ingest-catalogue.ps1
# options: -Path .\samples\books.csv   -DelayMs 500   -StopOnError
```

Скрипт читает CSV и отправляет POST по каждой строке в `Ingest Webhook`, поэтому каждая книга проходит **одну и ту же**
цепочку индексации: `Prepare Book Record` → `Delete Previous Chunks` → `Chunk Book Text` →
`Book Document Loader` → `Embeddings Google Gemini` → `Postgres PGVector Store (Insert)` →
`Upsert Book Metadata`.

> **Почему не триггер `Index Book Library`?** Этот путь **сломан в текущем виде**. `Read Book Catalogue`
> — это узел `extractFromFile`: он преобразует *входящие бинарные данные* в JSON, — и ничто выше него
> (голый ручной триггер) их не создаёт, поэтому он сразу падает. См. [SETUP.md](SETUP.md), шаг 7, для
> исправления, которое также требует `N8N_BLOCK_FILE_ACCESS_TO_N8N_FILES=false` и перезапуска n8n.

Текст разбивается на **1000 символов с перекрытием 200**. Метаданные каждого чанка несут
`book_id`, `title`, `author` и `genre`.

**Повторный запуск безопасен и не должен менять счётчики.** Цепочка удаляет старые чанки книги *до*
повторной вставки, а `Upsert Book Metadata` — это `ON CONFLICT (book_id) DO UPDATE`. Это
правило «upsert then prune» из RULES §3.4. `book_id` — ключ идемпотентности.

Проверьте результат:

```powershell
docker exec n8n-book-rag-db psql -U bookrag -d bookrag -c "select (select count(*) from books) as books, (select count(*) from book_chunks) as chunks;"
```

В `samples/books.csv` **20 книг** по 7 жанрам, поэтому ожидайте `books = 20` и `chunks > 0`.

### Колонки CSV

```
book_id, title, author, genre, tags, published_year, page_count, language, rating, description
```

`tags` могут быть разделены запятыми внутри поля (`"space-opera,politics,ecology"`).

> **Колонки позиционные — проверяйте каждую строку.** `samples/books.csv` поставлялся с 18 из 20 строк
> со сдвигом (автор дублировался перед названием, сдвигая каждую последующую колонку), а
> `Thinking, Fast and Slow` был без кавычек, поэтому разбился на два поля. Этот файл был исправлен. Когда вы
> добавляете строки, заключайте в кавычки любое поле, содержащее запятую, и сохраняйте порядок колонок, заданный заголовком, —
> сдвинутая строка не падает громко, она молча записывает теги в `published_year`.

---

## 3. Загрузка одной книги — API

`POST http://localhost:5678/webhook/book-rag/ingest`, та же аутентификация по заголовку, запускает *ту же* цепочку индексации,
поэтому добавленная здесь книга сразу доступна для рекомендаций.

```powershell
$headers = @{ 'x-book-rag-key' = '<WEBHOOK_HEADER_VALUE from .env>' }
$book = @{
  title          = 'Piranesi'
  author         = 'Susanna Clarke'
  genre          = 'Fantasy'
  tags           = @('mystery', 'quiet', 'worldbuilding')   # array, JSON string, or "a;b;c" all work
  description    = 'A lone scholar catalogues an infinite house of tides and statues.'
  content        = 'Longer excerpt or full text, if you have it. This is what gets chunked and embedded.'
  published_year = 2020
  page_count     = 245
  rating         = 4.2
  language       = 'en'
  book_url       = 'https://example.com/piranesi'
} | ConvertTo-Json

Invoke-RestMethod -Method Post `
  -Uri 'http://localhost:5678/webhook/book-rag/ingest' `
  -Headers $headers -ContentType 'application/json' -Body $book
```

Форма ответа (из `Respond to Ingest`):

```json
{ "ok": true, "book_id": "susanna-clarke-piranesi", "title": "Piranesi", "book_url": "https://example.com/piranesi" }
```

### Справочник по полям

| Поле | Обязательно | Примечания |
|---|---|---|
| `title` | рекомендуется | По умолчанию `Untitled`, если отсутствует. **Не опускайте его** — большая часть текста для поиска берётся из названия |
| `author` | рекомендуется | Используется в сгенерированном `book_id` и в эмбеддируемом тексте |
| `genre` | необязательно | Добавляется в начало эмбеддируемого текста как `[Genre]` |
| `tags` | необязательно | Массив, строка JSON-массива или `a,b,c` / `a;b;c` |
| `description` | необязательно, но важно | Обычно это всё семантическое содержимое, что есть, поэтому настоящее описание резко улучшает поиск (по векторам) |
| `content` | необязательно | Полный текст/отрывок; именно он разбивается на чанки. Длинные документы дают много чанков |
| `book_id` | необязательно | Ваш собственный стабильный id. Если опущен, он слагифицируется из автора + названия, поэтому **повторная загрузка той же книги обновляет, а не дублирует** |
| `published_year`, `page_count`, `rating` | необязательно | Приводятся к числам; всё нечисловое становится `null` |
| `language` | необязательно | По умолчанию `en` |
| `book_url` | необязательно | Возвращается обратно в ответе; полезно как ссылка в вашем собственном UI |

`book_id` — ключ идемпотентности. Укажите свой собственный (например, ISBN), если вы повторно отправляете ту же книгу из
приложения — иначе изменение названия создаст вторую запись.

---

## 4. Удаление книги

Два способа, и ведут они себя одинаково:

- **Через чат:** `Delete the book Piranesi.` Агент использует инструмент `Remove Book` и подтверждает название.
  Ему предписано использовать это **только** при явном запросе на удаление.
- **В SQL:** `delete_book('susanna-clarke-piranesi')` — функция удаляет строку каталога и её
  чанки вместе.

---

## 5. Повседневные команды

```powershell
# is the database up?
docker ps --filter name=n8n-book-rag-db

# row counts
docker exec n8n-book-rag-db psql -U bookrag -d bookrag -c "select (select count(*) from books) as books, (select count(*) from book_chunks) as chunks;"

# what is in the catalogue?
docker exec n8n-book-rag-db psql -U bookrag -d bookrag -c "select book_id, title, author, genre from books order by title;"

# open a SQL shell
docker exec -it n8n-book-rag-db psql -U bookrag -d bookrag

# is n8n alive?
(Invoke-WebRequest http://localhost:5678/healthz -UseBasicParsing).Content
```

---

## 6. Проверка качества — eval перед деплоем

Прежде чем менять модель, размер чанка или системный промпт, и перед тем как сказать «готово»,
прогоните набор eval. Он отвечает на вопрос «стало лучше или хуже» числом, а не впечатлением.

```powershell
# 1. Бесплатно и офлайн: проверить сам набор (ни docker, ни сети, ни ключа).
powershell -ExecutionPolicy Bypass -File .\scripts\eval-rag.ps1 -ValidateOnly

# 2. Измерить поиск: один эмбеддинг Gemini на вопрос, квота чат-модели не тратится.
powershell -ExecutionPolicy Bypass -File .\scripts\eval-rag.ps1

# 3. Перед деплоем: сквозная проверка через реальный webhook (дороже, ~3 запроса чата на вопрос).
powershell -ExecutionPolicy Bypass -File .\scripts\eval-rag.ps1 -Mode agent

# 4. Запомнить удачный результат как эталон — дальше прогоны будут сравниваться с ним.
powershell -ExecutionPolicy Bypass -File .\scripts\eval-rag.ps1 -PromoteBaseline
```

Вывод — таблица по категориям и список порогов:

```
category     questions  prec@k    recall@k  hit      MRR
ALL          30         0.74      0.77      0.9      0.9
self         5          0.8       0.8       0.8      0.8
refuse       1          0         -         -        -
```

`-` означает «не определено», а не ноль: у вопроса категории `refuse` нет ни попадания, ни MRR,
поэтому он не участвует в этих средних (см. [../evals/README.md](../evals/README.md) §3).
Код возврата `0` — все пороги пройдены, `1` — что-то просело. Отчёты (`.md` для чтения и `.json` для
машинного сравнения) складываются в `evals\results\`. Полное описание метрик, порогов, формата набора
и известных ограничений — в [../evals/README.md](../evals/README.md).

### Частые вопросы про eval

| `FAIL ... container n8n-book-rag-db is not running` | для режима `retrieve` нужна база | `.\scripts\start-db.ps1`, либо `-ValidateOnly`, если сеть не нужна |
|---|---|---|
| `FAIL ... GEMINI_API_KEY not found` | в `.env` нет ключа | добавьте `GEMINI_API_KEY` (используется только для эмбеддинга вопросов) |
| `429` посреди прогона | квота Gemini | подождите сброса или прогоняйте `-Mode retrieve` (дешевле) |
| Все `self`-вопросы провалены | сломан поиск (индекс, эмбеддинги, SQL), а не вкус модели | `.\scripts\verify-db.ps1`, затем `-Mode retrieve` на одном вопросе (`-K 1`) |
| Счёт падает после смены модели эмбеддингов | старые векторы недействительны | полная переиндексация каталога (RULES §5.6) |
| Нужно перепроверить пороги, не тратя квоту | есть сохранённый отчёт | `.\scripts\eval-rag.ps1 -InputResults .\evals\results\rag-eval-....json` |

---

## 7. Диагностика проблем

| Симптом | Причина | Исправление |
|---|---|---|
| Webhook возвращает `403` | Заголовок `x-book-rag-key` отсутствует или неверен | Значение должно совпадать с `WEBHOOK_HEADER_VALUE` в `.env` |
| Webhook возвращает `404` | Workflow не **Active**, или вы использовали `/webhook-test/`, когда прослушивание не идёт | Переключите workflow в Active; production-пути — `/webhook/book-rag/...` |
| Агент говорит, что у него нет книг | Пока ничего не проиндексировано | Выполните §2, подтвердите счётчики |
| Агент рекомендует книги, которых нет в библиотеке | Этого не должно происходить — промпт это запрещает | Сначала проверьте поиск (по векторам), выполнив поиск вручную; затем убедитесь, что системное сообщение узла агента всё ещё содержит инструкцию «NEVER invent titles» |
| `429` от Gemini | Квота бесплатного тарифа, особенно потому что чат и эмбеддинги **используют один и тот же ключ** | Дождитесь сброса в полночь по тихоокеанскому времени, уменьшите размер пакета (обработки) `Loop Over Books` или смените чат-модель на Groq (SETUP.md, шаг 3) |
| `expected 3072 dimensions, not N` | Несоответствие схемы/размерности эмбеддингов | Размерность — 3072 по состоянию на 2026-10-02. Приведите в соответствие `halfvec(3072)` в `db/01-schema.sql`, затем переиндексируйте |
| Поиск ничего не возвращает для жанра, который существует | Книга была загружена с описанием, но без `content`, или чанки слишком скудные | Проверьте `book_chunks` для этого `book_id`; более богатые `description`/`content` дают лучший поиск (по векторам) |
| Чат отвечает, но забывает предыдущую реплику | Разный `sessionId` при каждом вызове | Используйте тот же `sessionId`; память хранит последние 10 сообщений |
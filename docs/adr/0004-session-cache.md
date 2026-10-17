# ADR-0004. Кэш сессий аутентификации (Session Cache)

- **Статус:** принято
- **Дата:** 2026-10-17
- **Задача:** #31

## Контекст

При каждом защищённом gRPC/REST-запросе `interceptor.go:73` вызывает
`auth.Authenticate(ctx, token)`, который делает **2 запроса к PostgreSQL**:

1. `sessions.GetByTokenHash` — вычисляет SHA-256 токена и ищет строку по уникальному индексу.
2. `users.GetByID` — догружает пользователя по `session.UserID`.

1 000 RPS к API превращаются в **2 000 RPS** к БД. PostgreSQL — единственный
разделяемый ресурс инстансов сервиса: при горизонтальном масштабировании пул
становится точкой отказа, а не масштаба.

## Рассмотренные варианты

### Вариант A — Redis/KeyDB с TTL

- Ключ: `session:<sha256(token_hash)>` → сериализованный `{session_id, user_id, role, status, expires_at}`, `TTL = expires_at - now`.
- Атомарная инвалидация через `DEL` в `Logout`, `DeleteByUser`, `DeleteByUserExcept`, `DeactivateUser`, `ChangePassword`.
- PostgreSQL остаётся источником истины (warm-up после рестарта кэша, аудит).
- **Плюсы:** распределённый кэш, работает при масштабировании, единый источник кэша.
- **Минусы:** требует новой инфраструктуры (Redis в compose), дополнительная сеть и зависимость.

### Вариант B — локальный in-memory кэш с коротким TTL (выбран)

- `ristretto` — высокопроизводительный in-memory кэш с алгоритмом TinyLFU.
- Кэшируется пара `token_hash -> principal`, TTL 60s по умолчанию.
- Miss-caching: `ErrSessionNotFound` кэшируется на 5s для защиты от DoS.
- **Плюсы:** нет новой инфраструктуры, минимальный оверхед, простой деплой.
- **Минусы:** в окне TTL (60s) отозванный токен ещё валиден; нет sharing между инстансами.

### Вариант C — sync.Map (отклонён)

- `sync.Map` не поддерживает eviction и TTL — запись будет расти бесконечно.
- Ristretto использует SampledLFU eviction и встроенный freshness mechanism.

## Решение

**Вариант B — локальный in-memory кэш (ristretto)** как стартовая оптимизация.

### Архитектура

```
interceptor.go
    │
    ▼
Authenticate(ctx, token)
    │
    ├─ cache.Get(tokenHash) ─── hit ──► Principal (0 запросов к БД)
    │          │
    │         miss
    │          │
    │          ▼
    │   sessions.GetByTokenHash() ──► session
    │          │
    │          ▼
    │   users.GetByID() ──► user
    │          │
    │          ▼
    │   cache.Set(tokenHash, principal, expiresAt)
    │          │
    │          ▼
    │     Principal + Session
```

### Инвалидация

Кэш инвалидируется при каждой операции отзыва сессии:

| Операция | Метод | Метод кэша |
| --- | --- | --- |
| Logout | `service.Logout` | `InvalidateByUser(userID)` |
| ChangePassword | `service.ChangePassword` | `InvalidateByUser(userID)` |
| DeactivateUser | `service.changeStatus` | `InvalidateByUser(userID)` |
| RestoreUser | `service.changeStatus` | — (сессии не отзываются) |
| UpdateUser (смена статуса) | `service.UpdateUser` | `InvalidateByUser(userID)` |

### Конфигурация

Переменные окружения с префиксом `USER_SERVICE_SESSION_CACHE_*`:

| Переменная | По умолчанию | Назначение |
| --- | --- | --- |
| `USER_SERVICE_SESSION_CACHE_ENABLED` | `true` | включить/выключить кэш |
| `USER_SERVICE_SESSION_CACHE_TTL` | `60s` | TTL успешных записей |
| `USER_SERVICE_SESSION_CACHE_MISS_TTL` | `5s` | TTL miss-записей (защита от DoS) |
| `USER_SERVICE_SESSION_CACHE_MAX_COST` | `10000` | максимальное число записей |

### Метрики

Ristretto предоставляет встроенные метрики через `Cache.Metrics`:
- `Hits()` — число попаданий
- `Misses()` — число промахов
- `KeysAdded()` — число добавленных записей
- `KeysEvicted()` — число удалённых записей

## Последствия

### Положительные

- **Снижение нагрузки на PostgreSQL:** при попадании в кэш — 0 запросов к БД на запрос.
- **Нет новой инфраструктуры:** ristretto — обычная библиотека Go, не требует дополнительных контейнеров.
- **Bounded window отзыва:** miss-caching на 5s гарантирует, что неизвестный токен не будет hammer'ить БД.
- **Toggle + метрики:** кэш можно выключить конфигурцией; метрики hit/miss доступны через API ristretto.

### Отрицательные / ограничения

- **Окно отзывности:** отозванный токен остаётся валиден до истечения TTL (60s по умолчанию). Это задокументированное ограничение варианта B.
- **Нет sharing между инстансами:** каждый инстанс имеет свой кэш. При масштабировании hit rate будет ниже.
- **Memory usage:** кэш потребляет память процесса. MaxCost=10000 entries — примерно 100-200 MB с overhead ristretto.

### Не снимается выбором кэша

- Инвалидация кэша должна покрывать все пути отзыва сессии — это проверяется тестами.
- При переходе на Redis (вариант A) код кэша остаётся совместим — достаточно заменить backend.

## Целевая архитектура

Вариант A (Redis/KeyDB) остаётся целевым для горизонтального масштабирования.
Переход потребует:

1. Добавить Redis в `docker-compose.yml` (issue #6).
2. Заменить `ristretto.Cache` на `github.com/go-redis/redis/v9` с тем же интерфейсом.
3. Обновить ADR-0004 на статус "пересмотрено".

## Ссылки

- `services/user-service/internal/cache/session_cache.go` — реализация кэша.
- `services/user-service/internal/service/service.go` — внедрение кэша в AuthenticateSession.
- `services/user-service/README.md` — документация кэша.
- Issue #31 — исходная задача.
- Issue #6 — контейнеризация (цель: добавить Redis в compose).

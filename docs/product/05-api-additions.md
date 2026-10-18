# 05. API Additions

## Обзор

Этот файл описывает новые и изменённые gRPC-методы и REST-маршруты по сервисам. Для каждого RPC указаны:
- **Публичность:** `public` (без auth), `authenticated` (нужен Bearer-токен), `internal` (только через gRPC от других сервисов), `gateway-only` (через API Gateway, не напрямую).
- **REST-маршрут:** из аннотации `google.api.http`.
- **События:** какие доменные события публикует.

---

## book-service

### `AddWishlistItem`

```proto
rpc AddWishlistItem(AddWishlistItemRequest) returns (WishlistItem) {
  option (google.api.http) = {
    post: "/v1/wishlist"
    body: "*"
  };
}
```

| Параметр | Значение |
| --- | --- |
| Публичность | authenticated (READER) |
| REST | `POST /v1/wishlist` |
| События | публикует `book.wishlisted` через `pkg/events` |

```json
// Request (POST /v1/wishlist)
{
  "book_id": "550e8400-e29b-41d4-a716-446655440000"
}

// Response
{
  "id": "660e8400-e29b-41d4-a716-446655440001",
  "book_id": "550e8400-e29b-41d4-a716-446655440000",
  "added_at": "2026-11-01T10:00:00Z"
}
```

### `RemoveWishlistItem`

```proto
rpc RemoveWishlistItem(RemoveWishlistItemRequest) returns (google.protobuf.Empty) {
  option (google.api.http) = {
    delete: "/v1/wishlist/{book_id}"
  };
}
```

| Параметр | Значение |
| --- | --- |
| Публичность | authenticated (READER, только свой) |
| REST | `DELETE /v1/wishlist/{book_id}` |
| События | нет |

### `ListWishlist`

```proto
rpc ListWishlist(ListWishlistRequest) returns (ListWishlistResponse) {
  option (google.api.http) = {get: "/v1/wishlist"};
}
```

| Параметр | Значение |
| --- | --- |
| Публичность | authenticated (READER, только свои) |
| REST | `GET /v1/wishlist` |
| События | нет |

### `RateBook`

```proto
rpc RateBook(RateBookRequest) returns (Rating) {
  option (google.api.http) = {
    post: "/v1/ratings"
    body: "*"
  };
}
```

| Параметр | Значение |
| --- | --- |
| Публичность | authenticated (READER, только свои) |
| REST | `POST /v1/ratings` |
| События | публикует `book.rated` через `pkg/events` |

```json
// Request
{
  "book_id": "550e8400-...",
  "score": 5,
  "review": "Отличная книга!"
}
```

### `SimilarBooks` (ai-service, не book-service)

```proto
rpc SimilarBooks(SimilarBooksRequest) returns (SimilarBooksResponse) {
  option (google.api.http) = {get: "/v1/ai/books/{book_id}/similar"};
}
```

> **Перенос:** в `ai.v1.AiService` (#93) вместо `book.v1.BookService`. Причина: векторы хранятся в pgvector (`library-ai-rag-db`), который владеет ai-service; чтение чужой БД нарушает database-per-service. REST-путь изменён на `/v1/ai/books/{book_id}/similar`.

| Параметр | Значение |
| --- | --- |
| Публичность | public (без auth, публичная витрина) |
| REST | `GET /v1/ai/books/{book_id}/similar` |
| События | не публикует |

### `SearchBooks` (ai-service, не book-service)

```proto
rpc Search(SearchRequest) returns (SearchResponse) {
  option (google.api.http) = {get: "/v1/ai/search"};
}
```

> **Перенос:** в `ai.v1.AiService` (#93) вместо `book.v1.BookService`. Аналогичные причины: pgvector принадлежит ai-service, циклическая зависимость модулей. REST-путь изменён на `/v1/ai/search`.

| Параметр | Значение |
| --- | --- |
| Публичность | public (без auth) |
| REST | `GET /v1/ai/search?q=...&limit=20` |
| События | не публикует |

> **Изменение:** расширяет `ListBooks` из `proto/book/v1/book.proto`. Новый RPC вместо перезаписи существующего.

### `GetSignalAnalytics` (internal)

```proto
rpc GetSignalAnalytics(GetSignalAnalyticsRequest) returns (SignalAnalyticsResponse) {
  option (google.api.http) = {get: "/v1/analytics/signals"};
  option (grpc.gateway.protoc_gen_openapiv2.options.openapiv2_operation) = {
    security: { security_requirement: { key: "BearerAuth" } }
  };
}
```

| Параметр | Значение |
| --- | --- |
| Публичность | authenticated (LIBRARIAN, только свои данные) |
| REST | `GET /v1/analytics/signals` |
| События | нет (read-only projection) |

```json
// Response
{
  "popular_books": [
    { "book_id": "...", "title": "...", "views": 150, "wishlist_adds": 23, "ratings_avg": 4.2 }
  ],
  "wishlist_gap": [
    { "book_id": "...", "title": "...", "wishlist_count": 15, "available": false }
  ]
}
```

---

## user-service

### `CreateClub`

```proto
rpc CreateClub(CreateClubRequest) returns (Club) {
  option (google.api.http) = {
    post: "/v1/clubs"
    body: "*"
  };
  option (grpc.gateway.protoc_gen_openapiv2.options.openapiv2_operation) = {
    security: { security_requirement: { key: "BearerAuth" } }
  };
}
```

| Параметр | Значение |
| --- | --- |
| Публичность | authenticated (LIBRARIAN или назначенный читатель) |
| REST | `POST /v1/clubs` |
| События | публикует `club.created` через `pkg/events` |

### `JoinClub`

```proto
rpc JoinClub(JoinClubRequest) returns (ClubMembership) {
  option (google.api.http) = {
    post: "/v1/clubs/{club_id}/join"
    body: "*"
  };
  option (grpc.gateway.protoc_gen_openapiv2.options.openapiv2_operation) = {
    security: { security_requirement: { key: "BearerAuth" } }
  };
}
```

| Параметр | Значение |
| --- | --- |
| Публичность | authenticated (READER, только ACTIVE клуб) |
| REST | `POST /v1/clubs/{club_id}/join` |
| События | публикует `club.joined` через `pkg/events` |

### `LeaveClub`

```proto
rpc LeaveClub(LeaveClubRequest) returns (google.protobuf.Empty) {
  option (google.api.http) = {post: "/v1/clubs/{club_id}/leave"};
  option (grpc.gateway.protoc_gen_openapiv2.options.openapiv2_operation) = {
    security: { security_requirement: { key: "BearerAuth" } }
  };
}
```

| Параметр | Значение |
| --- | --- |
| Публичность | authenticated (READER, только свой членство) |
| REST | `POST /v1/clubs/{club_id}/leave` |
| События | публикует `club.left` через `pkg/events` |

### `PostToClub`

```proto
rpc PostToClub(PostToClubRequest) returns (ClubPost) {
  option (google.api.http) = {
    post: "/v1/clubs/{club_id}/posts"
    body: "*"
  };
  option (grpc.gateway.protoc_gen_openapiv2.options.openapiv2_operation) = {
    security: { security_requirement: { key: "BearerAuth" } }
  };
}
```

| Параметр | Значение |
| --- | --- |
| Публичность | authenticated (член клуба) |
| REST | `POST /v1/clubs/{club_id}/posts` |
| События | публикует `club.post.created` через `pkg/events` |

### `ModerateClubPost`

```proto
rpc ModerateClubPost(ModerateClubPostRequest) returns (google.protobuf.Empty) {
  option (google.api.http) = {
    post: "/v1/clubs/{club_id}/posts/{post_id}/moderate"
    body: "*"
  };
  option (grpc.gateway.protoc_gen_openapiv2.options.openapiv2_operation) = {
    security: { security_requirement: { key: "BearerAuth" } }
  };
}
```

| Параметр | Значение |
| --- | --- |
| Публичность | authenticated (LIBRARIAN или CURATOR клуба) |
| REST | `POST /v1/clubs/{club_id}/posts/{post_id}/moderate` |
| События | публикует `club.post.moderated` через `pkg/events` |

### `ListClubs`

```proto
rpc ListClubs(ListClubsRequest) returns (ListClubsResponse) {
  option (google.api.http) = {get: "/v1/clubs"};
}
```

| Параметр | Значение |
| --- | --- |
| Публичность | public (без auth, публичные клубы) |
| REST | `GET /v1/clubs` |
| События | нет |

### `GetClub`

```proto
rpc GetClub(GetClubRequest) returns (Club) {
  option (google.api.http) = {get: "/v1/clubs/{id}"};
}
```

| Параметр | Значение |
| --- | --- |
| Публичность | public (без auth) |
| REST | `GET /v1/clubs/{id}` |
| События | нет |

### `GenerateDiscussionQuestions` (internal)

```proto
rpc GenerateDiscussionQuestions(GenerateDiscussionQuestionsRequest) returns (GenerateDiscussionQuestionsResponse) {
  option (google.api.http) = {
    post: "/v1/clubs/{club_id}/questions/generate"
    body: "*"
  };
  option (grpc.gateway.protoc_gen_openapiv2.options.openapiv2_operation) = {
    security: { security_requirement: { key: "BearerAuth" } }
  };
}
```

| Параметр | Значение |
| --- | --- |
| Публичность | internal (вызывает ai-service через gRPC, REST-обёртка для библиотекарня) |
| REST | `POST /v1/clubs/{club_id}/questions/generate` |
| События | нет (вызывает ai-service) |

---

## ai-service

### `Recommend` (расширение)

```proto
rpc Recommend(RecommendRequest) returns (RecommendResponse) {
  option (google.api.http) = {
    post: "/v1/ai/recommendations"
    body: "*"
  };
  option (grpc.gateway.protoc_gen_openapiv2.options.openapiv2_operation) = {
    security: { security_requirement: { key: "BearerAuth" } }
  };
}
```

| Параметр | Значение |
| --- | --- |
| Публичность | authenticated (READER, LIBRARIAN) |
| REST | `POST /v1/ai/recommendations` |
| События | публикует `ai.recommend.shown` через `pkg/events` |

> **Изменение:** в ответ добавлено поле `explanation` (почему рекомендовано).

```json
// Response (расширение)
{
  "recommendation": {
    "query": "что-то по киберпанку",
    "answer": "Рекомендую Neuromancer — классика жанра.",
    "explanation": "Рекомендация на основе ваших оценок: вы оценили 5 звёзд «Дюну» и «Нейромант»...",
    "books": [
      { "book_id": "...", "title": "Neuromancer", "author": "William Gibson" }
    ],
    "source": "RECOMMENDATION_SOURCE_GENERATED",
    "session_id": "conv-abc123"
  }
}
```

### `AskAboutBook` (Phase 2, Companion)

```proto
rpc AskAboutBook(AskAboutBookRequest) returns (AskAboutBookResponse) {
  option (google.api.http) = {
    post: "/v1/ai/books/{book_id}/ask"
    body: "*"
  };
  option (grpc.gateway.protoc_gen_openapiv2.options.openapiv2_operation) = {
    security: { security_requirement: { key: "BearerAuth" } }
  };
}
```

| Параметр | Значение |
| --- | --- |
| Публичность | authenticated (READER, LIBRARIAN) |
| REST | `POST /v1/ai/books/{book_id}/ask` |
| События | публикует `ai.ask.shown` через `pkg/events` |

> **Phase 2:** Q&A по текущей книге без спойлеров. Ограничение по прочитанной части (читатель спрашивает «что имел в виду автор в главе 3?» → AI отвечает, ограничиваясь до главы 2).

```json
// Request
{
  "query": "Что автор хотел сказать в финале?",
  "book_id": "550e8400-...",
  "max_chapter_read": 2
}

// Response
{
  "answer": "Автор подводит читателя к мысли о...",
  "spoiler_warning": false
}
```

### `GenerateClubQuestions` (Phase 2)

```proto
rpc GenerateClubQuestions(GenerateClubQuestionsRequest) returns (GenerateClubQuestionsResponse) {
  option (google.api.http) = {
    post: "/v1/ai/club-questions"
    body: "*"
  };
  option (grpc.gateway.protoc_gen_openapiv2.options.openapiv2_operation) = {
    security: { security_requirement: { key: "BearerAuth" } }
  };
}
```

| Параметр | Значение |
| --- | --- |
| Публичность | authenticated (LIBRARIAN, CURATOR клуба) |
| REST | `POST /v1/ai/club-questions` |
| События | нет (ответ сохраняется в club_questions) |

```json
// Request
{
  "book_id": "550e8400-...",
  "count": 10
}

// Response
{
  "questions": [
    "Как вы думаете, почему герой принял такое решение?",
    "Какая тема книги ближе к вашему опыту?"
  ]
}
```

---

## loan-service (Phase 3)

### `CreateHold`

```proto
rpc CreateHold(CreateHoldRequest) returns (Hold) {
  option (google.api.http) = {
    post: "/v1/holds"
    body: "*"
  };
  option (grpc.gateway.protoc_gen_openapiv2.options.openapiv2_operation) = {
    security: { security_requirement: { key: "BearerAuth" } }
  };
}
```

| Параметр | Значение |
| --- | --- |
| Публичность | authenticated (READER) |
| REST | `POST /v1/holds` |
| События | публикует `hold.created` через `pkg/events` |

### `ListHolds`

```proto
rpc ListHolds(ListHoldsRequest) returns (ListHoldsResponse) {
  option (google.api.http) = {get: "/v1/holds"};
  option (grpc.gateway.protoc_gen_openapiv2.options.openapiv2_operation) = {
    security: { security_requirement: { key: "BearerAuth" } }
  };
}
```

| Параметр | Значение |
| --- | --- |
| Публичность | authenticated (READER, только свои) |
| REST | `GET /v1/holds` |
| События | нет |

### `CancelHold`

```proto
rpc CancelHold(CancelHoldRequest) returns (google.protobuf.Empty) {
  option (google.api.http) = {post: "/v1/holds/{hold_id}/cancel"};
  option (grpc.gateway.protoc_gen_openapiv2.options.openapiv2_operation) = {
    security: { security_requirement: { key: "BearerAuth" } }
  };
}
```

| Параметр | Значение |
| --- | --- |
| Публичность | authenticated (READER, только свои) |
| REST | `POST /v1/holds/{hold_id}/cancel` |
| События | публикует `hold.cancelled` через `pkg/events` |

### `ListFines` (Phase 3)

```proto
rpc ListFines(ListFinesRequest) returns (ListFinesResponse) {
  option (google.api.http) = {get: "/v1/fines"};
  option (grpc.gateway.protoc_gen_openapiv2.options.openapiv2_operation) = {
    security: { security_requirement: { key: "BearerAuth" } }
  };
}
```

| Параметр | Значение |
| --- | --- |
| Публичность | authenticated (LIBRARIAN — все, READER — свои) |
| REST | `GET /v1/fines` |
| События | нет |

### `PayFine` (Phase 3)

```proto
rpc PayFine(PayFineRequest) returns (PayFineResponse) {
  option (google.api.http) = {
    post: "/v1/fines/{fine_id}/pay"
    body: "*"
  };
  option (grpc.gateway.protoc_gen_openapiv2.options.openapiv2_operation) = {
    security: { security_requirement: { key: "BearerAuth" } }
  };
}
```

| Параметр | Значение |
| --- | --- |
| Публичность | authenticated (LIBRARIAN, READER — свои) |
| REST | `POST /v1/fines/{fine_id}/pay` |
| События | публикует `fine.paid` через `pkg/events` |

---

## Сводная таблица: публичность

| Сервис | RPC | Публичность | Gateway |
| --- | --- | --- | --- |
| book-service | `AddWishlistItem` | authenticated (READER) | ✅ |
| book-service | `RemoveWishlistItem` | authenticated (READER) | ✅ |
| book-service | `ListWishlist` | authenticated (READER) | ✅ |
| book-service | `RateBook` | authenticated (READER) | ✅ |
| book-service | `GetSignalAnalytics` | authenticated (LIBRARIAN) | ✅ |
| user-service | `CreateClub` | authenticated (LIBRARIAN) | ✅ |
| user-service | `JoinClub` | authenticated (READER) | ✅ |
| user-service | `LeaveClub` | authenticated (READER) | ✅ |
| user-service | `PostToClub` | authenticated (член клуба) | ✅ |
| user-service | `ModerateClubPost` | authenticated (LIBRARIAN) | ✅ |
| user-service | `ListClubs` | public | ✅ |
| user-service | `GetClub` | public | ✅ |
| user-service | `GenerateDiscussionQuestions` | internal (LIBRARIAN) | ✅ |
| ai-service | `Recommend` | authenticated | ✅ |
| ai-service | `Search` | public | ✅ |
| ai-service | `SimilarBooks` | public | ✅ |
| ai-service | `ExplainRecommendation` | authenticated | ✅ |
| ai-service | `TrackRecommendClick` | public | ✅ |
| ai-service | `AskAboutBook` | authenticated (Phase 2) | ✅ |
| ai-service | `GenerateClubQuestions` | authenticated (LIBRARIAN) | ✅ |
| ai-service | `IngestBook` | authenticated (LIBRARIAN) | ✅ |
| ai-service | `DeleteBook` | authenticated (LIBRARIAN) | ✅ |
| ai-service | `IndexStatus` | authenticated (LIBRARIAN) | ✅ |
| loan-service | `ReportReadingProgress` | authenticated (READER) | ✅ |
| loan-service | `CreateHold` | authenticated (READER, Phase 3) | ✅ |
| loan-service | `ListHolds` | authenticated (READER, Phase 3) | ✅ |
| loan-service | `CancelHold` | authenticated (READER, Phase 3) | ✅ |
| loan-service | `ListFines` | authenticated (Phase 3) | ✅ |
| loan-service | `PayFine` | authenticated (Phase 3) | ✅ |

---

## Ссылки

- Существующие контракты: `services/book-service/proto/book/v1/book.proto`, `services/user-service/proto/user/v1/user.proto`, `services/ai-service/proto/ai/v1/ai.proto`, `services/loan-service/proto/loan/v1/loan.proto`
- Модель данных: `docs/product/04-data-model.md`
- Сигналы: `docs/product/06-signals-and-analytics.md`

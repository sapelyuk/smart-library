# E2E Tests

Go test suite for end-to-end integration testing of the Smart Library platform.

## Running tests

```bash
# 1. Start the Docker stack
docker compose up -d

# 2. Run E2E tests
cd e2e
go test -v -count=1 -timeout=3m ./...
```

Tests automatically skip if any service is unreachable (safe for CI without a running stack).

## Test suite

| Test | Covers |
| --- | --- |
| `TestUserFlow` | User Service: login, register, /auth/me, RBAC (wrong password, no token) |
| `TestBookCatalog` | Book Service: CreateBook, AddBookCopy, ListBooks, GetBook, ListBookCopies |
| `TestLoanLifecycle` | Loan Service: Borrow, duplicate reject, List, Get, Renew, Return, ListOverdue |
| `TestGatewayRouting` | API Gateway: /healthz, routing to user-service and book-service, auth enforcement |
| `TestNotificationEventConsumption` | Notification Service: queue bindings, event publish → consume via RabbitMQ |
| `TestAIService` | AI Service: /healthz, IndexStatus, Recommend (skipped if n8n unreachable) |
| `TestFullFlow` | End-to-end happy path: register → create book → borrow → return |

## Configuration

Service URLs are configurable via environment variables (defaults shown):

| Variable | Default |
| --- | --- |
| `E2E_USER_URL` | `http://localhost:8092` |
| `E2E_BOOK_URL` | `http://localhost:8091` |
| `E2E_LOAN_URL` | `http://localhost:8093` |
| `E2E_GW_URL` | `http://localhost:8080` |
| `E2E_AI_URL` | `http://localhost:8095` |
| `E2E_RABBIT_URL` | `http://localhost:15672` |

## Prerequisites

- Docker stack must be running (`docker compose up -d`)
- Seed librarian must be configured (`.env`: `USER_SERVICE_SEED_LIBRARIAN_EMAIL=admin@library.local`)
- For `TestAIService` Recommend: n8n must be running at `host.docker.internal:5678`

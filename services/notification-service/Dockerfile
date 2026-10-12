# Build stage.
FROM golang:1.27-alpine AS builder

# Set working directory to repo root because go.mod files use replace directives
# that point to sibling modules (../../pkg).
WORKDIR /src

# Copy workspace and module files first for better layer caching.
COPY go.work go.work.sum* ./
COPY pkg/ ./pkg/
COPY services/notification-service/ ./services/notification-service/

# Build.
RUN GOWORK=/src/go.work \
    CGO_ENABLED=0 \
    GOOS=linux \
    go build -o /notification-service ./services/notification-service/cmd/server

# Runtime stage.
FROM alpine:3.21

RUN addgroup -g 10001 notification && \
    adduser -u 10001 -G notification -s /bin/sh notification

COPY --from=builder /notification-service /notification-service

USER notification

HEALTHCHECK --interval=10s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://localhost:8094/healthz || exit 1

EXPOSE 8084 8094

ENTRYPOINT ["/notification-service"]

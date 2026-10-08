# syntax=docker/dockerfile:1

# ============================================================
# Stage 1: build the service binary
# ============================================================
FROM golang:1.27-alpine AS builder

ENV GOTOOLCHAIN=local
ENV GOWORK=off

ARG MODULE=services/loan-service
ARG BIN_NAME=loan-service

# The loan module replaces three sibling modules: pkg, book-service and
# user-service, because it imports their generated proto packages. Every replace
# target has to exist at the path the directive names before `go mod download`
# runs, otherwise Go cannot resolve the build list (each of them now carries its
# own go.sum). The paths mirror the layout of the repository, so
# `../book-service` from /src lands on /book-service.
COPY pkg/ /pkg/
COPY services/book-service/go.mod services/book-service/go.sum /book-service/
COPY services/user-service/go.mod services/user-service/go.sum /user-service/

COPY ${MODULE}/go.mod ${MODULE}/go.sum /src/
WORKDIR /src
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# Now the sources. The generated proto packages of the sibling services are
# imported by the loan binary, so their code is needed to build it.
COPY pkg/ /pkg/
COPY services/book-service/ /book-service/
COPY services/user-service/ /user-service/
COPY ${MODULE}/ /src/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/${BIN_NAME} ./cmd/server

# ============================================================
# Stage 2: minimal runtime image
# ============================================================
FROM alpine:3.21 AS runtime

RUN apk add --no-cache wget

RUN adduser --disabled-password --home /app --shell /bin/sh --uid 10003 app

COPY --from=builder /out/loan-service /app/loan-service

WORKDIR /app
USER app

# Container healthcheck: HTTP liveness endpoint.
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -q --spider http://127.0.0.1:8093/healthz || exit 1

EXPOSE 8083 8093

ENTRYPOINT ["/app/loan-service"]

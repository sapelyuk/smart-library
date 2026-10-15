# syntax=docker/dockerfile:1

# ============================================================
# Stage 1: build the service binary
# ============================================================
FROM golang:1.27-alpine AS builder

# The module declares `go 1.26.0` in its go.mod, which this image satisfies.
# GOTOOLCHAIN=local forbids downloading a newer toolchain from the network.
ENV GOTOOLCHAIN=local
# The services are built one module at a time with replace directives pointing
# to ../../pkg, so the go.work workspace is intentionally disabled here.
ENV GOWORK=off

# Build args: the module lives at services/api-gateway. The build context is
# the repo root, which is required to resolve the module's replace directives.
ARG MODULE=services/api-gateway
ARG BIN_NAME=api-gateway

# The gateway module replaces three sibling modules: pkg, book-service and
# user-service, because its generated proto packages and auth code import
# their generated proto packages. Every replace target has to exist at the
# path the directive names before `go mod download` runs, otherwise Go cannot
# resolve the build list. The paths mirror the repository layout, so
# `../book-service` from /src lands on /book-service.
COPY pkg/ /pkg/
COPY services/book-service/go.mod services/book-service/go.sum /book-service/
COPY services/user-service/go.mod services/user-service/go.sum /user-service/

# Copy the service module definition and download its dependencies. This
# layer stays cached as long as go.mod and go.sum are unchanged.
COPY ${MODULE}/go.mod ${MODULE}/go.sum /src/
WORKDIR /src
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# Now the sources. The generated proto packages of the sibling services are
# imported by the gateway binary, so their code is needed to build it.
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

# wget is used by the container healthcheck below.
RUN apk add --no-cache wget

# Run as an unprivileged user, not root.
RUN adduser --disabled-password --home /app --shell /bin/sh --uid 10001 app

# The server binary does not read any config files (all settings come from
# environment variables), so only the executable is copied.
COPY --from=builder /out/api-gateway /app/api-gateway

WORKDIR /app
USER app

# Container healthcheck: HTTP liveness endpoint.
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -q --spider http://127.0.0.1:8080/healthz || exit 1

EXPOSE 8080

ENTRYPOINT ["/app/api-gateway"]

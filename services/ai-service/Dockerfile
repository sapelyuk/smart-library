# syntax=docker/dockerfile:1

# ---- build stage -------------------------------------------------------------
FROM golang:1.25-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/ai-service ./cmd/server

# ---- runtime stage -----------------------------------------------------------
FROM alpine:3.22

RUN apk add --no-cache ca-certificates && \
    adduser -D -u 10001 appuser

COPY --from=build /out/ai-service /usr/local/bin/ai-service

USER appuser

ENV PORT=8090

EXPOSE 8090

ENTRYPOINT ["ai-service"]

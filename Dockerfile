# syntax=docker/dockerfile:1.7

FROM golang:1.24-bookworm AS build

WORKDIR /src
ENV CGO_ENABLED=1

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/pushboy ./cmd/pushboy

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --system --gid 10001 pushboy \
    && useradd --system --uid 10001 --gid pushboy --home-dir /nonexistent --shell /usr/sbin/nologin pushboy

WORKDIR /app

COPY --from=build /out/pushboy /app/pushboy
COPY --from=build /src/db/migrations /app/db/migrations
RUN chown -R pushboy:pushboy /app/db/migrations \
    && chmod -R a+rX /app/db/migrations

ENV SERVER_PORT=:8080 \
    DATABASE_DRIVER=postgres

USER pushboy:pushboy
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD curl -fsS http://127.0.0.1:8080/v1/ping || exit 1

ENTRYPOINT ["/app/pushboy"]

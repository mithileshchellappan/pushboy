# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM golang:1.24-bookworm AS build

WORKDIR /src
ENV CGO_ENABLED=0

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=unknown
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false \
    -ldflags="-s -w -X main.version=$VERSION -X main.revision=$REVISION" -o /out/pushboy ./cmd/pushboy

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

ENV SERVER_PORT=:8080

USER pushboy:pushboy
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD curl -fsS http://127.0.0.1:8080/v1/ping || exit 1

ENTRYPOINT ["/app/pushboy"]

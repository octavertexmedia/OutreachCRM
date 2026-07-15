# OutReachCRM — static Go binary (≤30 MB gate via make build-size).
FROM golang:1.25-bookworm AS builder

WORKDIR /src

ENV CGO_ENABLED=0 \
    GOOS=linux

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG APP_VERSION=2.0.0
RUN go build -trimpath -ldflags="-s -w" -o /outreachcrm ./cmd/server

FROM debian:bookworm-slim

RUN apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
    ca-certificates gosu \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

RUN useradd --create-home --uid 10001 appuser \
  && mkdir -p /data \
  && chown appuser:appuser /data

COPY --from=builder /outreachcrm /app/outreachcrm
RUN chown appuser:appuser /app/outreachcrm

COPY docker-entrypoint.sh /docker-entrypoint.sh
RUN chmod +x /docker-entrypoint.sh

ENV ADDR=:8080 \
    DATA_DIR=/data \
    APP_VERSION=2.0.0

EXPOSE 8080

ENTRYPOINT ["/docker-entrypoint.sh"]
CMD ["/app/outreachcrm"]

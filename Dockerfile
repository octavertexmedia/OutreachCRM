# OutReachCRM — Go + Alibaba Zvec hybrid search (CGO).
FROM golang:1.25-bookworm AS builder

WORKDIR /src

RUN apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
    build-essential ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

ENV CGO_ENABLED=1 \
    GOOS=linux

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG APP_VERSION=2.0.0
ARG ZVEC_VERSION=v0.5.1

# Fetch zvec-go + prebuilt linux amd64 libs, then build with -tags zvec
RUN ZVEC_VERSION=${ZVEC_VERSION} ./scripts/setup-zvec.sh \
 && printf 'go 1.25.0\n\nuse (\n\t.\n\t./third_party/zvec-go\n)\n' > go.work \
 && go build -tags zvec -trimpath -ldflags="-s -w -X main.version=${APP_VERSION}" -o /outreachcrm ./cmd/server \
 && mkdir -p /zvec-lib \
 && cp -a third_party/zvec-go/lib/linux_amd64/. /zvec-lib/

FROM debian:bookworm-slim

RUN apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
    ca-certificates gosu \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

RUN useradd --create-home --uid 10001 appuser \
  && mkdir -p /data /app/lib \
  && chown appuser:appuser /data

COPY --from=builder /outreachcrm /app/outreachcrm
COPY --from=builder /zvec-lib/ /app/lib/
RUN chown appuser:appuser /app/outreachcrm \
  && chmod 755 /app/lib/*

COPY docker-entrypoint.sh /docker-entrypoint.sh
RUN chmod +x /docker-entrypoint.sh

ENV ADDR=:8080 \
    DATA_DIR=/data \
    APP_VERSION=2.0.0 \
    LD_LIBRARY_PATH=/app/lib

EXPOSE 8080

ENTRYPOINT ["/docker-entrypoint.sh"]
CMD ["/app/outreachcrm"]

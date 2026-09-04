APP=outreachcrm
# Raised for Alibaba Zvec (CGO + hybrid HNSW/FTS). Native lib is still a separate dylib/so.
MAX_BYTES=83886080
ZVEC_VERSION?=v0.5.1

.PHONY: run build build-size build-lite setup-zvec tidy test smartlead-import postgres-up postgres-down postgres-migrate

# Default: full Zvec hybrid search (dense HNSW + FTS + MultiQuery RRF).
run: setup-zvec
	@printf 'go 1.25.0\n\nuse (\n\t.\n\t./third_party/zvec-go\n)\n' > go.work
	CGO_ENABLED=1 go run -tags zvec ./cmd/server

tidy:
	go mod tidy

setup-zvec:
	ZVEC_VERSION=$(ZVEC_VERSION) ./scripts/setup-zvec.sh

build: setup-zvec
	@printf 'go 1.25.0\n\nuse (\n\t.\n\t./third_party/zvec-go\n)\n' > go.work
	CGO_ENABLED=1 go build -tags zvec -ldflags="-s -w" -o bin/$(APP) ./cmd/server
	@echo "Built with Alibaba Zvec hybrid search (HNSW + FTS + RRF)."

# Pure-Go SQLite FTS5 fallback (no CGO) — for environments without a C toolchain.
build-lite:
	rm -f go.work go.work.sum
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/$(APP) ./cmd/server

build-size: build
	@size=$$(stat -f%z bin/$(APP) 2>/dev/null || stat -c%s bin/$(APP)); \
	ls -lh bin/$(APP); \
	echo "bytes=$$size limit=$(MAX_BYTES)"; \
	if [ "$$size" -gt "$(MAX_BYTES)" ]; then echo "ERROR: binary exceeds size cap"; exit 1; fi; \
	echo "OK: under size cap ($$(( $(MAX_BYTES) / 1024 / 1024 )) MB)"

# One-shot Smartlead → SQLite import (no sequencer). Example:
#   SMARTLEAD_API_KEY=... make smartlead-import
#   go run ./cmd/smartlead-import --data-dir data --api-key-file ../smatlead-update/samartlead-api-key-latest.txt --dry-run
smartlead-import:
	CGO_ENABLED=0 go run ./cmd/smartlead-import --data-dir "$(or $(DATA_DIR),data)" $(ARGS)

test:
	@if [ -d third_party/zvec-go ]; then \
	  printf 'go 1.25.0\n\nuse (\n\t.\n\t./third_party/zvec-go\n)\n' > go.work; \
	  CGO_ENABLED=1 go test -tags zvec ./...; \
	else \
	  go test ./...; \
	fi

# Local pgvector (search index only). Operational CRM data stays in SQLite.
#   export DATABASE_URL='postgres://outreach:outreach@127.0.0.1:55432/outreachcrm?sslmode=disable'
postgres-up:
	docker compose -f docker-compose.postgres.yml up -d postgres
	@echo "Wait for healthy, then: DATABASE_URL=postgres://outreach:outreach@127.0.0.1:55432/outreachcrm?sslmode=disable"

postgres-down:
	docker compose -f docker-compose.postgres.yml down

# Copy an existing SQLite CRM into Postgres, preserving row ids. Run this
# BEFORE pointing the app at DATABASE_URL, or the CRM comes up empty.
#   make postgres-migrate ARGS=--dry-run
postgres-migrate:
	CGO_ENABLED=0 go run ./cmd/sqlite-to-postgres --data-dir "$(or $(DATA_DIR),data)" $(ARGS)

APP=outreachcrm
# Raised for Alibaba Zvec (CGO + hybrid HNSW/FTS). Native lib is still a separate dylib/so.
MAX_BYTES=83886080
ZVEC_VERSION?=v0.5.1

.PHONY: run build build-size build-lite setup-zvec tidy test

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

test:
	@if [ -d third_party/zvec-go ]; then \
	  printf 'go 1.25.0\n\nuse (\n\t.\n\t./third_party/zvec-go\n)\n' > go.work; \
	  CGO_ENABLED=1 go test -tags zvec ./...; \
	else \
	  go test ./...; \
	fi

APP=outreachcrm
MAX_BYTES=31457280

.PHONY: run build build-size tidy test

run:
	go run ./cmd/server

tidy:
	go mod tidy

build:
	CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=$(APP_VERSION)" -o bin/$(APP) ./cmd/server

build-size: build
	@size=$$(stat -f%z bin/$(APP) 2>/dev/null || stat -c%s bin/$(APP)); \
	ls -lh bin/$(APP); \
	echo "bytes=$$size limit=$(MAX_BYTES)"; \
	if [ "$$size" -gt "$(MAX_BYTES)" ]; then echo "ERROR: binary exceeds 30MB"; exit 1; fi; \
	echo "OK: under 30MB"

test:
	go test ./...

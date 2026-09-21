VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION)"
GOFLAGS := -trimpath

# Canarium uses a pure-Go SQLite driver, so cgo is never required. Building
# with CGO_ENABLED=0 everywhere keeps the binary static and identical across
# host and cross-compiled targets.
export CGO_ENABLED = 0

.PHONY: all build build-frontend build-go test test-race lint fmt vet govulncheck audit check clean distclean dev dist

all: build

build-frontend:
	cd web && npm ci && npm run build

build: build-frontend build-go

build-go:
	go build $(GOFLAGS) $(LDFLAGS) -o canarium ./cmd/canarium

test:
	go test ./...

# The race detector is implemented in C, so this target is the one place
# cgo is required. Everything else builds with CGO_ENABLED=0 so the binary
# stays static and cross-compiles.
test-race:
	CGO_ENABLED=1 go test -race -count=1 ./...

fmt:
	gofmt -w ./cmd ./internal ./modules ./web.go

vet:
	go vet ./...

govulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

audit:
	cd web && npm audit --audit-level=high

lint:
	golangci-lint run

# Everything CI enforces, runnable locally before pushing.
check: vet lint test-race
	@unformatted=$$(gofmt -l ./cmd ./internal ./modules ./web.go); \
	if [ -n "$$unformatted" ]; then \
		echo "Not gofmt-formatted:"; echo "$$unformatted"; exit 1; \
	fi
	go mod tidy -diff

clean:
	rm -f canarium
	rm -rf dist web/dist

# Remove node_modules too. Separate from `clean` because reinstalling is slow.
distclean: clean
	rm -rf web/node_modules

# Cross-compilation targets. All depend on the frontend, which is embedded
# into the binary at compile time.
dist/canarium-linux-amd64: build-frontend
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) $(LDFLAGS) -o $@ ./cmd/canarium

dist/canarium-linux-arm64: build-frontend
	GOOS=linux GOARCH=arm64 go build $(GOFLAGS) $(LDFLAGS) -o $@ ./cmd/canarium

dist/canarium-linux-arm: build-frontend
	GOOS=linux GOARCH=arm GOARM=7 go build $(GOFLAGS) $(LDFLAGS) -o $@ ./cmd/canarium

dist: dist/canarium-linux-amd64 dist/canarium-linux-arm64 dist/canarium-linux-arm

dev:
	go run ./cmd/canarium run --config examples/basic.yaml

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION)"
GOFLAGS := -trimpath

.PHONY: all build build-frontend test lint clean dev

all: build

build-frontend:
	cd web && npm ci && npm run build

build: build-frontend
	CGO_ENABLED=1 go build $(GOFLAGS) $(LDFLAGS) -o canarium ./cmd/canarium

build-go:
	CGO_ENABLED=1 go build $(GOFLAGS) $(LDFLAGS) -o canarium ./cmd/canarium

test:
	CGO_ENABLED=1 go test ./...

lint:
	golangci-lint run

clean:
	rm -f canarium
	rm -rf web/dist web/node_modules

dev:
	CGO_ENABLED=1 go run ./cmd/canarium run --config examples/basic.yaml

# Cross-compilation targets
dist/canarium-linux-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS) $(LDFLAGS) -o $@ ./cmd/canarium

dist/canarium-linux-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GOFLAGS) $(LDFLAGS) -o $@ ./cmd/canarium

dist/canarium-linux-arm:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build $(GOFLAGS) $(LDFLAGS) -o $@ ./cmd/canarium

dist: dist/canarium-linux-amd64 dist/canarium-linux-arm64 dist/canarium-linux-arm

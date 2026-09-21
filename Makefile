.PHONY: build clean test lint install build-all release-all release-linux release-darwin release-windows compress-releases

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS = -s -w -X main.version=$(VERSION)

# Default target - static build with optimizations
build:
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o bin/ccusage_go ./cmd/ccusage

# Clean build artifacts
clean:
	rm -rf bin/
	go clean

# Run tests
test:
	go test -v ./...

# Run tests with coverage
test-coverage:
	go test -v -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# Run benchmarks
benchmark:
	go test -bench=. -benchmem ./...

# Lint code
lint:
	golangci-lint run

# Install to GOPATH
install:
	go install -ldflags="$(LDFLAGS)" ./cmd/ccusage

# Six supported static release targets; archives include the MIT license.
build-all: release-all

release-linux:
	VERSION=$(VERSION) GOOS=linux GOARCH=amd64 sh scripts/build-release.sh
	VERSION=$(VERSION) GOOS=linux GOARCH=arm64 sh scripts/build-release.sh

release-darwin:
	VERSION=$(VERSION) GOOS=darwin GOARCH=amd64 sh scripts/build-release.sh
	VERSION=$(VERSION) GOOS=darwin GOARCH=arm64 sh scripts/build-release.sh

release-windows:
	VERSION=$(VERSION) GOOS=windows GOARCH=amd64 sh scripts/build-release.sh
	VERSION=$(VERSION) GOOS=windows GOARCH=arm64 sh scripts/build-release.sh

release-all: release-linux release-darwin release-windows
	cd dist && sha256sum -- *.tar.gz *.zip > checksums.txt

# Archives are created by each release target.
compress-releases: release-all

# Run go mod tidy
tidy:
	go mod tidy

# Format code
fmt:
	go fmt ./...

# Vet code
vet:
	go vet ./...

# All quality checks
check: fmt vet test

# Development build with debug info
dev:
	go build -gcflags="all=-N -l" -o bin/ccusage_go-dev ./cmd/ccusage

# Dynamic build (non-static, smaller size)
dynamic:
	go build -ldflags="$(LDFLAGS)" -o bin/ccusage_go-dynamic ./cmd/ccusage

# pi5_exporter — build/test/lint
#
# Version metadata is injected into github.com/prometheus/common/version so the
# exporter exposes pi5_exporter_build_info and `--version` prints sensible values.

BINARY      := pi5_exporter
PKG         := github.com/bcrisp4/pi5_exporter
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
REVISION    ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BRANCH      ?= $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)
BUILD_DATE  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
COMMON      := github.com/prometheus/common/version

LDFLAGS := -s -w \
	-X $(COMMON).Version=$(VERSION) \
	-X $(COMMON).Revision=$(REVISION) \
	-X $(COMMON).Branch=$(BRANCH) \
	-X $(COMMON).BuildDate=$(BUILD_DATE)

.PHONY: all build test test-race test-hw lint fmt vet tidy clean run

all: lint test build

build: ## Build the binary for the host arch
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) .

build-arm64: ## Cross/native build for arm64 (the Pi 5 target)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) .

test: ## Unit tests (hermetic — no hardware)
	go test ./...

test-race: ## Unit tests with the race detector
	go test -race ./...

test-hw: ## Hardware integration tests — RUN ONLY ON A PI 5 (needs /dev/vcio + video group)
	# No -race: the Pi's 47-bit-VMA kernel is incompatible with Go's TSan runtime.
	go test -tags pi5_hardware ./...

lint: vet fmt ## Static checks

vet:
	go vet ./...

fmt: ## Fail if any file is not gofmt-clean
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi

tidy:
	go mod tidy

run: build ## Build and run locally on :2712
	./$(BINARY) --collection.interval=2s

clean:
	rm -f $(BINARY)

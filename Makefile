VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BINARY  := burst-core
INSTALL_DIR := $(HOME)/.burst/bin

LDFLAGS := -X github.com/scttfrdmn/burst-core/cmd/burst-core/internal/version.Version=$(VERSION)

.PHONY: build build-all test test-integration lint install clean

build:
	go build -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/burst-core

build-all:
	goreleaser build --snapshot --clean

test:
	go test ./...

test-integration:
	BURST_INTEGRATION_TEST=1 go test ./...

lint:
	golangci-lint run

install: build
	mkdir -p $(INSTALL_DIR)
	cp $(BINARY) $(INSTALL_DIR)/$(BINARY)
	@echo "Installed to $(INSTALL_DIR)/$(BINARY)"

release:
	@if [ -z "$(TAG)" ]; then echo "Usage: make release TAG=v0.1.0"; exit 1; fi
	git tag -s $(TAG) -m "Release $(TAG)"
	git push origin $(TAG)

clean:
	rm -f $(BINARY)
	rm -rf dist/

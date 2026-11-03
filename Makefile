# Universal Clipboard — developer entry points.
GO      ?= go
BIN     := bin/universal-clipboard
PREFIX  ?= $(HOME)/.local
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
LDFLAGS := -s -w -X github.com/universal-clipboard/universal-clipboard/daemon/internal/application.Version=$(VERSION)

.PHONY: all build test test-short e2e vet fmt cross install uninstall clean

all: vet test build

build:
	cd daemon && CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o ../$(BIN) ./cmd/universal-clipboard

test:
	cd daemon && $(GO) test -race ./...

test-short:
	cd daemon && $(GO) test ./...

e2e:
	cd daemon && $(GO) test -tags e2e -count=1 ./tests/e2e/

vet:
	cd daemon && $(GO) vet ./... && $(GO) vet -tags e2e ./tests/e2e/
	@test -z "$$(cd daemon && gofmt -l .)" || (echo "gofmt needed:"; cd daemon && gofmt -l .; exit 1)

fmt:
	cd daemon && gofmt -w .

cross:
	@for target in linux/amd64 linux/arm64 windows/amd64 darwin/arm64 darwin/amd64; do \
		os=$${target%/*}; arch=$${target#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "building $$os/$$arch"; \
		(cd daemon && CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags '$(LDFLAGS)' \
			-o ../dist/universal-clipboard-$$os-$$arch$$ext ./cmd/universal-clipboard) || exit 1; \
	done

install: build
	install -Dm755 $(BIN) $(PREFIX)/bin/universal-clipboard
	@echo "Installed to $(PREFIX)/bin. Next: universal-clipboard service install"

uninstall:
	-$(PREFIX)/bin/universal-clipboard service uninstall
	rm -f $(PREFIX)/bin/universal-clipboard

clean:
	rm -rf bin dist

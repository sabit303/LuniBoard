# LuniBoard — developer entry points.
GO      ?= go
BIN     := bin/luniboard
PREFIX  ?= $(HOME)/.local
VERSION ?= $(patsubst v%,%,$(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev))
LDFLAGS := -s -w -X github.com/luniboard/luniboard/daemon/internal/application.Version=$(VERSION)

.PHONY: all build test test-short e2e vet fmt cross release install uninstall clean

all: vet test build

build:
	cd daemon && CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o ../$(BIN) ./cmd/luniboard

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
			-o ../dist/luniboard-$$os-$$arch$$ext ./cmd/luniboard) || exit 1; \
	done

# Linux release tarballs consumed by packaging/linux/arch/luniboard-bin/PKGBUILD:
#   dist/luniboard-$(VERSION)-linux-{amd64,arm64}.tar.gz
release:
	@rm -rf dist/release && mkdir -p dist/release
	@for arch in amd64 arm64; do \
		name=luniboard-$(VERSION)-linux-$$arch; dir=dist/release/$$name; \
		echo "packaging $$name"; mkdir -p $$dir; \
		(cd daemon && CGO_ENABLED=0 GOOS=linux GOARCH=$$arch $(GO) build -p 2 -trimpath -ldflags '$(LDFLAGS)' \
			-o ../$$dir/luniboard ./cmd/luniboard) || exit 1; \
		cp packaging/linux/systemd/luniboard.service LICENSE README.md daemon/configs/config.example.toml $$dir/; \
		tar -C dist/release --owner=0 --group=0 --sort=name -czf dist/$$name.tar.gz $$name || exit 1; \
	done
	@rm -rf dist/release
	@cd dist && sha256sum luniboard-$(VERSION)-linux-*.tar.gz | tee luniboard-$(VERSION)-SHA256SUMS

install: build
	install -Dm755 $(BIN) $(PREFIX)/bin/luniboard
	@echo "Installed to $(PREFIX)/bin. Next: luniboard service install"

uninstall:
	-$(PREFIX)/bin/luniboard service uninstall
	rm -f $(PREFIX)/bin/luniboard

clean:
	rm -rf bin dist

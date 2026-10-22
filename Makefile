# skilltri build/test/install. `make install` puts the binary at $(BINDIR)/skilltri (default ~/.local/bin).
BINDIR ?= $(HOME)/.local/bin
VERSION ?= 0.1.0
GO ?= go
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test test-unit vet fmt install clean screenshots

build:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/skilltri .

# all tests
test: vet
	$(GO) test -race -count=1 ./...

# alias of test (target name kept for convention)
test-unit: vet
	$(GO) test -race -count=1 ./...

vet:
	@test -z "$$(gofmt -l .)" || (gofmt -l .; echo "gofmt needed"; exit 1)
	$(GO) vet ./...

fmt:
	gofmt -w .

install:
	mkdir -p $(BINDIR)
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BINDIR)/skilltri .
	@echo "installed $(BINDIR)/skilltri"

clean:
	rm -rf bin

# README screenshots (docs/screenshots) from the VHS tapes in docs/tapes, with demo data in a sandbox
screenshots:
	bash docs/screenshots.sh

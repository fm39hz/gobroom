BIN      := gorouter
DAEMON   := gorouterd
TUI      := gorouter-tui
VERSION  := $(shell git describe --tags --long --dirty --match 'v*' 2>/dev/null | sed -E 's/^v//; s/-([0-9]+)-g/.r\1.g/; s/-/./g')
LDFLAGS  := -s -w $(if $(VERSION),-X main.version=$(VERSION))
REMOTE   := origin
BRANCH   := master

.PHONY: help build build-daemon build-tui build-all run run-daemon tui test test-v race bench fmt vet install install-all clean reload status

help: ## list targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}; {printf "  %-16s %s\n", $$1, $$2}'

build: ## build CLI
	go build -ldflags='$(LDFLAGS)' -o $(BIN) ./cmd/gorouter

build-daemon: ## build daemon
	go build -ldflags='$(LDFLAGS)' -o $(DAEMON) ./cmd/gorouterd

build-tui: ## build TUI frontend
	go build -ldflags='$(LDFLAGS)' -o $(TUI) ./cmd/gorouter-tui

build-all: build build-daemon build-tui ## build CLI, daemon and TUI

run: ## run CLI (ARGS='status')
	go run -ldflags='$(LDFLAGS)' ./cmd/gorouter $(ARGS)

run-daemon: ## run daemon in foreground
	go run -ldflags='$(LDFLAGS)' ./cmd/gorouterd $(ARGS)

tui: ## run TUI
	go run -ldflags='$(LDFLAGS)' ./cmd/gorouter-tui

status: ## query daemon status over IPC
	go run ./cmd/gorouter status

reload: ## reload daemon snapshot over IPC
	go run ./cmd/gorouter reload

test: ## unit and integration tests
	go test ./...

test-v: ## verbose tests
	go test ./... -count=1 -v

race: ## race detector
	go test -race ./...

bench: ## benchmarks
	go test ./... -bench=. -benchmem -run=^$$

fmt: ## gofmt all Go files
	gofmt -w $$(find . -name '*.go' -not -path './tools/*')

vet: ## go vet
	go vet ./...

install: build build-daemon ## install CLI and daemon into GOPATH/bin
	go install -ldflags='$(LDFLAGS)' ./cmd/gorouter
	go install -ldflags='$(LDFLAGS)' ./cmd/gorouterd

install-all: install ## install binaries and user systemd unit
	mkdir -p ~/.config/systemd/user
	cp dist/gorouterd.service ~/.config/systemd/user/gorouterd.service
	systemctl --user daemon-reload
	systemctl --user enable gorouterd 2>/dev/null || true
	systemctl --user restart gorouterd

clean: ## remove local binaries
	rm -f $(BIN) $(DAEMON) $(TUI)

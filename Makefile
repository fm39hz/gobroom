BIN      := gobroom
DAEMON   := gobroomd
TUI      := gobroom-tui
VERSION  := $(shell git describe --tags --long --dirty --match 'v*' 2>/dev/null | sed -E 's/^v//; s/-([0-9]+)-g/.r\1.g/; s/-/./g')
LDFLAGS  := -s -w $(if $(VERSION),-X main.version=$(VERSION))
REMOTE   := origin
BRANCH   := master

.PHONY: help build build-daemon build-tui build-all run run-daemon tui test test-v race bench fmt vet install install-all clean reload status

help: ## list targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}; {printf "  %-16s %s\n", $$1, $$2}'

build: ## build CLI
	go build -ldflags='$(LDFLAGS)' -o $(BIN) ./cmd/gobroom

build-daemon: ## build daemon
	go build -ldflags='$(LDFLAGS)' -o $(DAEMON) ./cmd/gobroomd

build-tui: ## build TUI frontend
	go build -ldflags='$(LDFLAGS)' -o $(TUI) ./cmd/gobroom-tui

build-all: build build-daemon build-tui ## build CLI, daemon and TUI

run: ## run CLI (ARGS='status')
	go run -ldflags='$(LDFLAGS)' ./cmd/gobroom $(ARGS)

run-daemon: ## run daemon in foreground
	go run -ldflags='$(LDFLAGS)' ./cmd/gobroomd $(ARGS)

tui: ## run TUI
	go run -ldflags='$(LDFLAGS)' ./cmd/gobroom-tui

status: ## query daemon status over IPC
	go run ./cmd/gobroom status

reload: ## reload daemon snapshot over IPC
	go run ./cmd/gobroom reload

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

install: build build-daemon build-tui ## install CLI, daemon and TUI into GOPATH/bin
	go install -ldflags='$(LDFLAGS)' ./cmd/gobroom
	go install -ldflags='$(LDFLAGS)' ./cmd/gobroomd
	go install -ldflags='$(LDFLAGS)' ./cmd/gobroom-tui

install-all: install ## install binaries and user systemd unit
	mkdir -p ~/.config/systemd/user
	cp dist/gobroomd.service ~/.config/systemd/user/gobroomd.service
	systemctl --user daemon-reload
	systemctl --user enable gobroomd 2>/dev/null || true
	systemctl --user restart gobroomd

clean: ## remove local binaries
	rm -f $(BIN) $(DAEMON) $(TUI)

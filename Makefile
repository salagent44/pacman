.PHONY: build static test run

VERSION ?= $(shell git describe --always --dirty 2>/dev/null || echo dev)
LDFLAGS  = -X main.version=$(VERSION)

build:
	go build -ldflags "$(LDFLAGS)" -o pacman .

static:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w $(LDFLAGS)" -o pacman .

test:
	go vet ./... && go test ./...

run: build
	PACMAN_TOKEN=devtoken ./pacman serve -addr :8080 -dir ./data

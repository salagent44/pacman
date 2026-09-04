.PHONY: build static test run

build:
	go build -o pacman .

static:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o pacman .

test:
	go vet ./... && go test ./...

run: build
	PACMAN_TOKEN=devtoken ./pacman -addr :8080 -dir ./data

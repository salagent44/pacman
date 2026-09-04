.PHONY: build static test run publish release

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

# build and push this binary into our own drop; machines pick it up with `pacman update`
publish: static
	./pacman put ./pacman

# build linux amd64+arm64 and cut a GitHub release with gh (no CI, ever)
release:
	@test -n "$(VERSION)" || { echo "usage: make release VERSION=vX.Y.Z"; exit 1; }
	rm -rf dist && mkdir -p dist
	for arch in amd64 arm64; do \
	  CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath \
	    -ldflags "-s -w -X main.version=$(VERSION)" -o dist/pacman-linux-$$arch . ; \
	done
	cd dist && sha256sum pacman-linux-* > SHA256SUMS
	gh release create $(VERSION) dist/pacman-linux-amd64 dist/pacman-linux-arm64 dist/SHA256SUMS \
	  --title $(VERSION) --generate-notes

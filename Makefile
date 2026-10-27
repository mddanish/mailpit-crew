VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test release clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o mailpit-crew ./cmd/mailpit-crew

test:
	go vet ./...
	go test ./...

release:
	mkdir -p dist
	for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/mailpit-crew-linux-$$arch ./cmd/mailpit-crew; \
	done

clean:
	rm -rf mailpit-crew dist

MODULE := github.com/findardi/system-engineer-road/go-mosh
DIST   := cmd/gosh-client/dist
GOFLAGS_REL := -trimpath -ldflags="-s -w"
PREFIX ?= $(HOME)/.local

.PHONY: build server install proto clean

build: server
	CGO_ENABLED=0 go build $(GOFLAGS_REL) -o bin/ ./cmd/gosh-client

# Server must be built first: the client embeds these binaries.
server:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build $(GOFLAGS_REL) -o $(DIST)/gosh-server-linux-amd64 ./cmd/gosh-server
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build $(GOFLAGS_REL) -o $(DIST)/gosh-server-linux-arm64 ./cmd/gosh-server

install: build
	install -Dm755 bin/gosh-client $(PREFIX)/bin/gosh

proto:
	protoc -I proto --go_out=. --go_opt=module=$(MODULE) proto/*.proto

clean:
	rm -rf bin $(DIST)/gosh-server-*

MODULE := github.com/findardi/system-engineer-road/go-mosh

.PHONY: build proto clean

build:
	CGO_ENABLED=0 go build -o bin/ ./cmd/...

proto:
	protoc -I proto --go_out=. --go_opt=module=$(MODULE) proto/*.proto

clean:
	rm -rf bin

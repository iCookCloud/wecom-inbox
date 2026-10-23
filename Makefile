VERSION ?= dev
BINARY := wecom-inbox
.PHONY: build test check release
build:
	CGO_ENABLED=0 go build -trimpath -ldflags '-s -w -X main.version=$(VERSION)' -o $(BINARY) ./cmd/wecom-inbox
test:
	go test ./... -count=1
check:
	go vet ./...
	go test -race ./... -count=1
release:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '-s -w -X main.version=$(VERSION)' -o dist/wecom-inbox-linux-amd64 ./cmd/wecom-inbox
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags '-s -w -X main.version=$(VERSION)' -o dist/wecom-inbox-linux-arm64 ./cmd/wecom-inbox
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags '-s -w -X main.version=$(VERSION)' -o dist/wecom-inbox-darwin-arm64 ./cmd/wecom-inbox
	cp LICENSE THIRD_PARTY_NOTICES.md dist/
	cd dist && sha256sum wecom-inbox-* LICENSE THIRD_PARTY_NOTICES.md > SHA256SUMS

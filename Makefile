.PHONY: build test check

build:
	CGO_ENABLED=0 go build -trimpath -o build/filewise ./cmd/filewise

test:
	go test -race -count=1 ./...

check:
	@test -z "$$(gofmt -l cmd internal)"
	go vet ./...
	go test -race -count=1 ./...

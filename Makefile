.PHONY: build test lint check run-mcp clean

build:
	go build -o bin/smarthelper ./cmd/smarthelper

test:
	go test ./...

lint:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	go vet ./...

check: lint test build

run-mcp: build
	./bin/smarthelper mcp

clean:
	rm -rf bin/

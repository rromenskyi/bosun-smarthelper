.PHONY: build test lint check run-mcp run-web clean

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

run-web: build
	./bin/smarthelper serve

clean:
	rm -rf bin/

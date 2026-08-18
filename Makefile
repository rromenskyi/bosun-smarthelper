.PHONY: build test lint check run-mcp run-web clean docker-build docker-up docker-down

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

# Builds all three images (bosun app, llama-chat, llama-embed) together —
# see docker-compose.yml and deploy/llama/Dockerfile. llama-chat/llama-embed
# share one image (bosun-llama-cpp:local); the first build compiles
# llama.cpp from source and is slow, later ones hit layer cache.
docker-build:
	docker compose build

docker-up: docker-build
	docker compose up -d

docker-down:
	docker compose down

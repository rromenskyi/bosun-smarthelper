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

# Builds every image, including sandboxd/the run_code runtime image even
# though that feature is off by default (docs/sandbox.md) — cheap, and
# means enabling it later (sandbox.enabled + COMPOSE_PROFILES=sandbox) is
# just a config edit, not another build. --profile sandbox is needed
# because sandboxd carries a Compose profile precisely so `docker compose
# up -d` (no profile) never starts it; `build` respects profiles the same
# way `up` does. bosun-sandbox-python:local is a plain `docker build`, not
# a Compose service — it never runs on its own, so it has no service to
# attach a profile to. llama-chat/llama-embed share one image
# (bosun-llama-cpp:local); the first build compiles llama.cpp from source
# and is slow, later ones hit layer cache.
docker-build:
	docker compose --profile sandbox build
	docker build -t bosun-sandbox-python:local ./deploy/sandbox-runtime

docker-up: docker-build
	docker compose up -d

docker-down:
	docker compose down

# syntax=docker/dockerfile:1

# sandboxd (docs/sandbox.md) — the only process in this stack that holds
# /var/run/docker.sock. Self-contained, like deploy/llama/Dockerfile and
# deploy/whisper/Dockerfile, rather than derived from the main Dockerfile:
# builds its own copy of the same `smarthelper` binary (it only ever runs
# the `sandbox-serve` subcommand) so this image's dependency surface never
# grows just because the main bosun image's does.
FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -trimpath -o /out/smarthelper ./cmd/smarthelper

FROM alpine:edge
# docker-cli: the real Docker client this service shells out to
# (internal/sandbox/runner.go) — no dockerd, no Docker Go SDK, matching
# this project's hand-rolled-over-heavy-SDK preference. Talks to the host
# daemon over the bind-mounted socket (docker-compose.yml).
RUN apk add --no-cache docker-cli ca-certificates
COPY --from=builder /out/smarthelper /usr/local/bin/smarthelper

ENTRYPOINT ["/usr/local/bin/smarthelper"]
CMD ["sandbox-serve"]

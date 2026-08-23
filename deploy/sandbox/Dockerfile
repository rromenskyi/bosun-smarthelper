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
#
# uid/gid 1000 to match this host's own user (roman220), same reasoning
# as the main Dockerfile's `bosun` user: sandboxd writes to a plain host
# directory (./data/sandbox, not a Docker-managed volume) for per-session
# workspaces and the reaper's state file, and root-owned files there would
# be unreadable/unremovable from the host (and break `go build ./...`,
# which walks the whole repo tree). Running as non-root here does NOT
# reduce sandboxd's own privilege — docker-compose.yml's `group_add`
# grants /var/run/docker.sock access via the host's "docker" group
# regardless of this container's own UID, the same pattern already used
# for the bosun service's /dev/ttyACM0 (group dialout) and /dev/snd
# (group audio) access.
RUN apk add --no-cache docker-cli ca-certificates && \
    addgroup -g 1000 sandboxd && adduser -S -u 1000 -G sandboxd -h /home/sandboxd -s /sbin/nologin sandboxd
COPY --from=builder /out/smarthelper /usr/local/bin/smarthelper

USER sandboxd
ENTRYPOINT ["/usr/local/bin/smarthelper"]
CMD ["sandbox-serve"]

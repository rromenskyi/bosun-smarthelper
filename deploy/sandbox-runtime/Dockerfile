# The image every run_code session's workspace container runs (docs/sandbox.md).
# Pinned by digest, not a floating tag — a `docker compose build` months
# from now must not silently change what's inside the isolation boundary,
# the same discipline deploy/llama/Dockerfile (pinned commit) and
# deploy/piper/Dockerfile (pinned PIPER_REF) already use. Resolved via
# `docker pull python:3.12-alpine && docker inspect --format '{{index .RepoDigests 0}}' python:3.12-alpine`
# on 2026-08-22; re-pin deliberately (a comment update, reviewed) rather
# than letting this drift.
FROM python:3.12-alpine@sha256:d09d15e60962ca365d1cd544a48773bac9d33f2fb1b00f2aa0deec78ade7dc31

# uid/gid 1000 to match this host's own user, same reasoning as
# deploy/sandbox/Dockerfile: /workspace is a bind-mounted plain host
# directory (docs/sandbox.md), and root-owned files in it would be
# unreadable/unremovable from the host without sudo. Not a security
# boundary — internal/sandbox/runner.go's process-group kill doesn't
# need root either way, only membership in the process's own group.
RUN addgroup -g 1000 sandbox && adduser -S -u 1000 -G sandbox sandbox
USER sandbox

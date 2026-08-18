# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -trimpath -o /out/smarthelper ./cmd/smarthelper

FROM alpine:3.20
# ca-certificates: outbound HTTPS (remote LLM, weather/search/wikipedia/maps
# geocoding all use it) needs a trust store even in a minimal image.
RUN apk add --no-cache ca-certificates && \
    addgroup -S bosun && adduser -S -G bosun -h /home/bosun -s /sbin/nologin bosun && \
    mkdir -p /home/bosun/.local/share/bosun && \
    chown -R bosun:bosun /home/bosun
# The directory above is created (and owned by bosun) before the volume
# mount point exists, so a fresh named volume mounted there inherits that
# ownership instead of defaulting to root — otherwise the non-root
# container user can't write memos/sessions/error log into it.

COPY --from=builder /out/smarthelper /usr/local/bin/smarthelper

USER bosun
ENV HOME=/home/bosun
WORKDIR /home/bosun

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/smarthelper"]
CMD ["serve"]

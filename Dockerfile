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
# poppler-utils: pdfinfo/pdftotext/pdftoppm — PDF document uploads are split
# page by page and rendered to images for pages with no text layer (see
# internal/webui/pdf.go, docs/memo-search.md). tesseract-ocr (+ eng/rus
# language data, matching this deployment's chat languages) then reads text
# off that rendered image, so a scanned page is still searchable by its
# actual content instead of just a generic page-number label.
#
# bosun is uid/gid 1000 specifically to match this host's own user
# (roman220) — persistent data is bind-mounted from a plain host directory
# (./data/bosun, see docker-compose.yml), not a Docker-managed named
# volume, so the whole stack (and its data) survives `docker compose down
# -v` or even removing Docker entirely. A host-owned uid/gid mismatch
# would otherwise leave the container unable to write to that directory.
RUN apk add --no-cache ca-certificates poppler-utils tesseract-ocr tesseract-ocr-data-eng tesseract-ocr-data-rus && \
    addgroup -g 1000 bosun && adduser -S -u 1000 -G bosun -h /home/bosun -s /sbin/nologin bosun && \
    mkdir -p /home/bosun/.local/share/bosun && \
    chown -R bosun:bosun /home/bosun
# The directory above is created (and owned by bosun) before the mount
# point exists, so it's already correctly owned the first time something
# gets bind-mounted or volume-mounted there.

COPY --from=builder /out/smarthelper /usr/local/bin/smarthelper

USER bosun
ENV HOME=/home/bosun
WORKDIR /home/bosun

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/smarthelper"]
CMD ["serve"]

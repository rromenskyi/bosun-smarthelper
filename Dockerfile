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
RUN apk add --no-cache ca-certificates poppler-utils tesseract-ocr tesseract-ocr-data-eng tesseract-ocr-data-rus && \
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

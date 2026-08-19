# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -trimpath -o /out/smarthelper ./cmd/smarthelper

# Piper TTS (docs/voice.md): built against Alpine's own musl-native
# onnxruntime package rather than Microsoft's official prebuilt binary,
# which is glibc-only and has no working Alpine/musl compatibility path —
# confirmed: gcompat gets far enough to load it, but a genuine C++
# exception-ABI mismatch between Alpine's musl-targeted libstdc++ and
# glibc's breaks error propagation partway through real synthesis.
# Alpine's onnxruntime package (musl-native, no ABI mismatch) only exists
# on the edge branch, hence alpine:edge here — pinned per-package below,
# not tracking edge as a rolling target.
FROM alpine:edge AS piper-builder
ARG PIPER_REF=v1.7.0
RUN apk add --no-cache build-base cmake git onnxruntime-dev
WORKDIR /src
RUN git clone --depth 1 --branch ${PIPER_REF} https://github.com/OHF-Voice/piper1-gpl.git .
# Patches piper_exe to emit 16-bit PCM WAV instead of upstream's IEEE
# float WAV (inconsistent browser <audio> support) — see
# deploy/piper/wav-pcm16.patch and docs/voice.md for how this was
# verified against the exact pinned PIPER_REF above.
COPY deploy/piper/wav-pcm16.patch /tmp/wav-pcm16.patch
RUN git apply /tmp/wav-pcm16.patch
# CMAKE_INSTALL_PREFIX unused here: piper_exe/libpiper.so are read
# straight out of the build tree below, matching how this was verified.
RUN cmake -B libpiper/build -S libpiper -DCMAKE_BUILD_TYPE=Release && \
    cmake --build libpiper/build -j"$(nproc)"

FROM alpine:edge
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
#
# onnxruntime: Piper TTS's runtime dependency (see the piper-builder stage
# above) — apk pulls in its transitive deps (protobuf-lite, re2, abseil,
# icu) automatically.
RUN apk add --no-cache ca-certificates poppler-utils tesseract-ocr tesseract-ocr-data-eng tesseract-ocr-data-rus onnxruntime && \
    addgroup -g 1000 bosun && adduser -S -u 1000 -G bosun -h /home/bosun -s /sbin/nologin bosun && \
    mkdir -p /home/bosun/.local/share/bosun && \
    chown -R bosun:bosun /home/bosun
# The directory above is created (and owned by bosun) before the mount
# point exists, so it's already correctly owned the first time something
# gets bind-mounted or volume-mounted there.

COPY --from=builder /out/smarthelper /usr/local/bin/smarthelper
# espeak-ng is statically linked into libpiper.so (see the piper-builder
# stage), so only the binary, the library, and its phoneme/dictionary
# data need copying — no separate espeak-ng runtime package.
COPY --from=piper-builder /src/libpiper/build/src/main/piper_exe /usr/local/bin/piper_exe
COPY --from=piper-builder /src/libpiper/build/libpiper.so /usr/local/lib/libpiper.so
COPY --from=piper-builder /src/libpiper/build/espeak_ng-install/share/espeak-ng-data /usr/local/share/espeak-ng-data
ENV LD_LIBRARY_PATH=/usr/local/lib

USER bosun
ENV HOME=/home/bosun
WORKDIR /home/bosun

EXPOSE 443
ENTRYPOINT ["/usr/local/bin/smarthelper"]
CMD ["serve"]

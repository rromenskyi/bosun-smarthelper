# syntax=docker/dockerfile:1
#
# Builds whisper-server from source, mirroring deploy/llama/Dockerfile
# exactly (same three GGML_* flags — whisper.cpp shares llama.cpp's ggml
# backend, so the same portable, runtime-dispatching CPU build applies
# unchanged): GGML_CPU_ALL_VARIANTS + GGML_NATIVE=OFF picks the right SIMD
# variant at startup (this host's is "sandybridge": SSE4.2+AVX, no
# AVX2/FMA/F16C) instead of assuming one and risking an illegal-instruction
# crash. Verified on this exact host — see docs/voice.md.
ARG WHISPER_CPP_REF=4834a2327d008ace3ec5a9ed00f51454bcabbc1c

FROM ubuntu:26.04 AS builder
ARG WHISPER_CPP_REF
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential cmake git ca-certificates libssl-dev pkg-config \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
RUN git clone https://github.com/ggml-org/whisper.cpp.git . && \
    git checkout "${WHISPER_CPP_REF}"
RUN cmake -B build \
    -DCMAKE_BUILD_TYPE=Release \
    -DGGML_NATIVE=OFF \
    -DGGML_BACKEND_DL=ON \
    -DGGML_CPU_ALL_VARIANTS=ON \
    -DWHISPER_SDL2=OFF && \
    cmake --build build -j"$(nproc)" --target whisper-server

FROM ubuntu:26.04
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates libssl3 libgomp1 && \
    rm -rf /var/lib/apt/lists/*
# The whole bin/ dir, not just the whisper-server binary: GGML_CPU_ALL_VARIANTS
# ships one libggml-cpu-*.so per microarchitecture next to the executable,
# and the right one is chosen by probing CPU features at process startup.
COPY --from=builder /src/build/bin/ /usr/local/lib/whisper.cpp/
RUN ln -s /usr/local/lib/whisper.cpp/whisper-server /usr/local/bin/whisper-server
ENV LD_LIBRARY_PATH=/usr/local/lib/whisper.cpp

# Reuse the base image's built-in "ubuntu" user (uid 1000) — matches the
# host user that owns the bind-mounted model directory (docker-compose.yml).
USER ubuntu
WORKDIR /home/ubuntu
ENTRYPOINT ["/usr/local/bin/whisper-server"]

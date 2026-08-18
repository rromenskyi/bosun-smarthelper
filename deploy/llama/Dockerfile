# syntax=docker/dockerfile:1
#
# Builds llama-server from source instead of pulling the official
# ghcr.io/ggml-org/llama.cpp image, mirroring this host's own
# ~/rebuild-llama.cpp.sh build exactly (same three GGML_* flags): the
# portable, runtime-dispatching CPU backend (GGML_CPU_ALL_VARIANTS,
# GGML_NATIVE=OFF — picks the right SIMD variant at startup instead of
# assuming one and risking an illegal-instruction crash on this specific,
# fussy CPU). Pin LLAMA_CPP_REF to the commit that script last built so the
# container runs the identical, already-verified build, not whatever HEAD
# happens to be on image build day.
ARG LLAMA_CPP_REF=0021a77de0a8966059dc94548fb3b96654e0bb12

FROM ubuntu:26.04 AS builder
ARG LLAMA_CPP_REF
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential cmake git ca-certificates libssl-dev pkg-config \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
RUN git clone https://github.com/ggml-org/llama.cpp.git . && \
    git checkout "${LLAMA_CPP_REF}"
RUN cmake -B build \
    -DCMAKE_BUILD_TYPE=Release \
    -DGGML_NATIVE=OFF \
    -DGGML_BACKEND_DL=ON \
    -DGGML_CPU_ALL_VARIANTS=ON && \
    cmake --build build -j"$(nproc)"

FROM ubuntu:26.04
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates libssl3 libgomp1 && \
    rm -rf /var/lib/apt/lists/*
# The whole bin/ dir, not just the llama-server binary: GGML_CPU_ALL_VARIANTS
# ships one libggml-cpu-*.so per microarchitecture next to the executable,
# and the right one is chosen by probing CPU features at process startup.
COPY --from=builder /src/build/bin/ /usr/local/lib/llama.cpp/
RUN ln -s /usr/local/lib/llama.cpp/llama-server /usr/local/bin/llama-server
ENV LD_LIBRARY_PATH=/usr/local/lib/llama.cpp

# Reuse the base image's built-in "ubuntu" user (uid 1000) rather than
# creating a new one — it already matches the host uid that owns the
# bind-mounted Hugging Face cache directory.
USER ubuntu
WORKDIR /home/ubuntu
ENTRYPOINT ["/usr/local/bin/llama-server"]

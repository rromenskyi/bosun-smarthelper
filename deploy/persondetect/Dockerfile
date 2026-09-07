# syntax=docker/dockerfile:1
#
# A small, local person-detector: YOLOv8n (COCO's smallest/fastest
# variant) behind a one-endpoint HTTP wrapper (server.py) — see
# docs/cameras.md's "Security mode" section for why this exists instead
# of routing every camera check through the vision-capable LLM path
# (internal/llm/vision.go): that's fine for an occasional "what's in
# this photo" ask, but far too slow/costly to poll every camera every
# few seconds.
#
# torch is installed from the CPU-only wheel index specifically — the
# default index resolves to the CUDA build, which is both useless on
# this CPU-only host and roughly 10x the download size.
FROM python:3.11-slim

# ultralytics pulls in plain opencv-python (not the headless variant),
# which dlopens a handful of X11/GL runtime libraries even though this
# server never opens a window — python:3.11-slim doesn't ship them.
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgl1 libglib2.0-0 libxcb1 libxrender1 libsm6 libxext6 \
    && rm -rf /var/lib/apt/lists/*

RUN pip install --no-cache-dir torch torchvision --index-url https://download.pytorch.org/whl/cpu && \
    pip install --no-cache-dir ultralytics

WORKDIR /app

# Baked into the image at build time rather than fetched on first
# request: this container has no reason to reach the internet at
# runtime, and a cold first request shouldn't pay a multi-second
# download plus have to handle it failing if the network's down.
# Must run after WORKDIR — YOLO('yolov8n.pt') resolves that relative
# path against the current directory, and server.py (run from the same
# WORKDIR) looks for it in exactly that spot.
RUN python3 -c "from ultralytics import YOLO; YOLO('yolov8n.pt')"

COPY server.py /app/server.py
ENV PYTHONUNBUFFERED=1
ENTRYPOINT ["python3", "server.py"]

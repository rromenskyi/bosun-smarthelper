"""Minimal HTTP wrapper around a YOLOv8n person detector.

Deliberately not a web framework — this is one endpoint doing one thing
for one internal caller (internal/persondetect.Client), so the stdlib's
http.server is simpler than adding Flask/FastAPI as a dependency.

POST /detect with a raw JPEG body -> {"person_detected": bool,
"count": int, "confidence": float} (confidence is the highest-scoring
person detection's score, 0.0 if none).
GET /health -> "ok", once the model has actually loaded (a camera
security checker retrying on a 503 during startup is simpler than it
guessing at a fixed warmup delay).
"""

import io
import json
import os
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from PIL import Image
from ultralytics import YOLO

PORT = int(os.environ.get("PORT", "8100"))
# Confidence floor for what counts as "a person" at all — filters out the
# low-confidence noise a general-purpose COCO model throws on a grainy,
# compressed webcam frame; not a knob a caller needs to see or tune.
CONFIDENCE_THRESHOLD = 0.5
COCO_PERSON_CLASS = 0

model = None
model_lock = threading.Lock()


def load_model():
    global model
    m = YOLO("yolov8n.pt")
    with model_lock:
        model = m
    print("model loaded", file=sys.stderr, flush=True)


class Handler(BaseHTTPRequestHandler):
    def log_message(self, format, *args):
        # The default logs every request to stderr with a timestamp
        # format this deployment's other services don't use — quiet by
        # default, same as the other local model servers.
        pass

    def do_GET(self):
        if self.path == "/health":
            with model_lock:
                ready = model is not None
            self.send_response(200 if ready else 503)
            self.end_headers()
            self.wfile.write(b"ok" if ready else b"loading")
            return
        self.send_response(404)
        self.end_headers()

    def do_POST(self):
        if self.path != "/detect":
            self.send_response(404)
            self.end_headers()
            return

        with model_lock:
            m = model
        if m is None:
            self.send_response(503)
            self.end_headers()
            self.wfile.write(b'{"error":"model still loading"}')
            return

        length = int(self.headers.get("Content-Length", 0))
        if length <= 0:
            self.send_response(400)
            self.end_headers()
            self.wfile.write(b'{"error":"empty body"}')
            return
        image_bytes = self.rfile.read(length)

        try:
            # predict() only decodes an already-loaded image (a PIL Image,
            # numpy array, ...) — a raw BytesIO of still-encoded JPEG
            # bytes isn't one of the source types it understands.
            image = Image.open(io.BytesIO(image_bytes)).convert("RGB")
            results = m.predict(source=image, verbose=False)
        except Exception as e:
            self.send_response(400)
            self.end_headers()
            self.wfile.write(json.dumps({"error": str(e)}).encode())
            return

        best_confidence = 0.0
        count = 0
        for r in results:
            for box in r.boxes:
                if int(box.cls[0]) != COCO_PERSON_CLASS:
                    continue
                confidence = float(box.conf[0])
                if confidence < CONFIDENCE_THRESHOLD:
                    continue
                count += 1
                best_confidence = max(best_confidence, confidence)

        body = json.dumps({
            "person_detected": count > 0,
            "count": count,
            "confidence": best_confidence,
        }).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


if __name__ == "__main__":
    # Loaded in the background so /health can report "loading" instead of
    # the server refusing connections outright while YOLO's weights load.
    threading.Thread(target=load_model, daemon=True).start()
    server = ThreadingHTTPServer(("0.0.0.0", PORT), Handler)
    print(f"listening on :{PORT}", file=sys.stderr, flush=True)
    server.serve_forever()

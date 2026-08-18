# Importing a CHARM-style service manual

`import_manual.py` bulk-loads a manual site's export (e.g. from
[Operation CHARM](https://charm.li)) into Bosun's document knowledge base
(`docs/memo-search.md`) — one document per chapter, real text chunked
normally, and diagram-only pages (fuse panels, wiring diagrams) rendered
as images with OCR'd text attached so they're findable by content, not
just a generic caption.

This is the script version of the pipeline first run by hand (chat
history, 2026-08-18) to load the **Ford E-350 1991 V8-460 7.5L** manual —
kept here so it doesn't have to be reconstructed from scratch next time.

## Prerequisites

- `python3` (stdlib only — no `pip install` needed)
- `tesseract` with English and Russian language data, **on the machine
  running this script** (not just in the Bosun container):
  ```bash
  sudo apt-get install tesseract-ocr tesseract-ocr-eng tesseract-ocr-rus
  ```
- `docker` access to the running Bosun container (for copying diagram
  images into its data volume) — pass `--docker-sudo` if that needs `sudo`
- Bosun itself reachable at `--bosun-url` (default `http://localhost:8080`)
  with a document store configured (`llm.embeddings` + `documents.path` —
  see `docs/memo-search.md`)

## Finding a bundle URL

On a manual's page (e.g. `https://charm.li/Ford/1991/E%20350%20Van%20V8-460%207.5L/`),
look for the "download for offline use — long filenames" link. It looks
like:
```
https://charm.li/bundle/long-names/Ford/1991/E%20350%20Van%20V8-460%207.5L/
```
Use the **long-names** variant, not the plain one — the plain bundle names
files by opaque numeric ID, which this script relies on real names for
(chapter directories, page titles).

## Usage

```bash
python3 import_manual.py \
  --bundle-url "https://charm.li/bundle/long-names/Ford/1991/E%20350%20Van%20V8-460%207.5L/" \
  --title-prefix "Ford E-350 1991 V8-460 7.5L" \
  --bosun-url http://localhost:8080 \
  --container bosun \
  --docker-sudo \
  --replace
```

Or against an already-downloaded/extracted bundle (skips the download):
```bash
python3 import_manual.py --bundle-zip ~/Downloads/manual.zip --title-prefix "..." ...
python3 import_manual.py --extracted-dir ~/Downloads/1991\ Ford\ E\ 350\ Van\ V8-460\ 7.5L --title-prefix "..." ...
```

Notes:
- `--section` defaults to `"Repair and Diagnosis"` (the actual
  procedures/specs/diagrams) and skips `"Parts and Labor"` (labor times,
  part numbers — not diagnostically useful). Pass `--section ""` to import
  the whole bundle instead.
- `--replace` deletes any existing document with the same title before
  creating it — needed to re-run without accumulating duplicates.
- `--skip-images` imports only the text chapters, skipping the
  OCR/diagram pipeline entirely (much faster — OCR over ~1000 images can
  take a couple of hours on weak hardware).
- `--work-dir` pins the download/extraction/OCR-cache directory instead of
  a throwaway temp dir — set this if you want to resume an interrupted OCR
  pass (`ocr-cache.json` inside it) without re-downloading or re-OCRing
  what already finished.

## What it does, step by step

1. Downloads and unzips the bundle (or reuses `--bundle-zip`/`--extracted-dir`).
2. Walks every page under `--section`, classifying each as a text page
   (real prose, no diagram) or an image page (has a `big-img`).
3. Dedupes text pages by content hash and image pages by
   `(caption, image hash)` — CHARM cross-lists the same content under
   multiple category trees, so without this the same TSB or diagram would
   be imported 2-3 times.
4. Groups text pages by chapter, uploads each chapter as one document via
   `POST /api/documents` (plain text — chunking happens server-side).
5. OCRs every *unique* image once (cached to `ocr-cache.json`, resumable).
6. Copies unique images into the Bosun container's
   `document-images` directory via `docker cp` (+ a `chown` fixup, since
   `docker cp` as root leaves files the container's non-root user can't
   otherwise touch).
7. Groups image pages by chapter, uploads each as one document via
   `POST /api/documents/pages` — a caption plus that image's OCR text
   becomes the page's `text`, `image_url` points at the copied file.

## Adapting this for a different manual/site

The HTML-parsing regexes (`MAIN_RE`, `H1_RE`, `IMG_RE`) are specific to
CHARM's page template (`<div class='main'>...<div class="theme-colors
footer">`, `<img class='big-img'>`). A different manual site will need
those adjusted to match its own markup — everything else (dedup, chunking,
OCR, upload) is site-agnostic.

# Importing a CHARM-style service manual

`import_manual.py` bulk-loads a manual site's export (e.g. from
[Operation CHARM](https://charm.li)) into Bosun's file dump with search
indexing (`docs/filedump.md`, `docs/memo-search.md`) — one text file per
manual chapter, one image file per unique diagram, everything under a
single file dump folder (the slugified `--title-prefix`) so it reads as
one topic, not one per chapter, in the dynamic topics prompt line
(`docs/settings.md`).

This is the script version of the pipeline first run by hand (chat
history, 2026-08-18) to load the **Ford E-350 1991 V8-460 7.5L** manual —
kept here so it doesn't have to be reconstructed from scratch next time.

## Prerequisites

- `python3` (stdlib only — no `pip install` needed)
- Bosun itself reachable at `--bosun-url` (default `http://localhost:8080`)
  with a document store configured (`llm.embeddings` — see
  `docs/memo-search.md`) and `filedump.path` set (`docs/filedump.md`)

OCR happens server-side (the same `POST /api/files/upload` path a
drag-and-dropped diagram would go through), so `tesseract` only needs to
be installed on the Bosun host, not on whatever machine runs this script.

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
- `--replace` deletes every existing document titled `"{title-prefix} —
  ..."` before importing — needed to re-run without accumulating
  duplicates. Matches by prefix, not exact per-chapter title, since a
  diagram chapter's title scheme changed (see below) from one document
  per chapter to one per image.
- `--skip-images` imports only the text chapters, skipping diagrams
  entirely (much faster).
- `--ocr-langs` (default `eng`) is passed through to the server per image
  upload — same tesseract language codes as `docs/memo-search.md`'s OCR
  section. Not `eng+rus` by default for the same reason
  `internal/webui/pdf.go`'s own default isn't: tesseract's combined model
  tends to misread plain English glyphs as look-alike Cyrillic ones,
  turning a real word into noise. Pass it explicitly for a manual that's
  actually in Russian.
- `--work-dir` pins the download/extraction directory instead of a
  throwaway temp dir — set this if you want to resume an interrupted run
  without re-downloading.

## What it does, step by step

1. Downloads and unzips the bundle (or reuses `--bundle-zip`/`--extracted-dir`).
2. Walks every page under `--section`, classifying each as a text page
   (real prose, no diagram) or an image page (has a `big-img`).
3. Dedupes text pages by content hash and image pages by
   `(caption, image hash)` — CHARM cross-lists the same content under
   multiple category trees, so without this the same TSB or diagram would
   be imported 2-3 times.
4. Groups text pages by chapter, uploads each chapter as one file dump
   text file (`{slug}/`) via `POST /api/files/upload` — chunking and
   embedding happen server-side.
5. Uploads each *unique* diagram image as its own file dump file
   (`{slug}/diagrams/`), titled with the first caption that referenced
   it — the server OCRs it and stores it as a one-page document
   (`internal/webui/pdf.go`'s `ingestStandaloneImage`), the same
   `{text, image_url}` shape a diagram embedded in a PDF gets.

## Adapting this for a different manual/site

The HTML-parsing regexes (`MAIN_RE`, `H1_RE`, `IMG_RE`) are specific to
CHARM's page template (`<div class='main'>...<div class="theme-colors
footer">`, `<img class='big-img'>`). A different manual site will need
those adjusted to match its own markup — everything else (dedup,
chunking, upload) is site-agnostic.

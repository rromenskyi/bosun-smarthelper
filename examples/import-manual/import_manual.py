#!/usr/bin/env python3
"""Import a CHARM-style ("Operation CHARM" / charm.li) car service manual
bundle into Bosun's file dump, with search indexing (see docs/filedump.md,
docs/memo-search.md) — one text file per manual chapter, one image file
per unique diagram. Everything lands under a single top-level file dump
folder (--title-prefix, slugified) so it groups as one topic in the
dynamic topics prompt line (docs/settings.md) instead of one per chapter.

This is the script version of the pipeline first run by hand (chat
history, 2026-08-18) to load the **Ford E-350 1991 V8-460 7.5L** manual —
kept here so it doesn't have to be reconstructed from scratch next time.
OCR now happens server-side (POST /api/files/upload's add_to_rag path,
internal/webui/pdf.go) rather than by this script, so tesseract no longer
needs to be installed on the machine running it — only on the Bosun host,
which already needs it for PDF ingestion.

See README.md in this directory for prerequisites and a full walkthrough.

Example:
    python3 import_manual.py \\
        --bundle-url "https://charm.li/bundle/long-names/Ford/1991/E%20350%20Van%20V8-460%207.5L/" \\
        --bosun-url http://localhost:8080 \\
        --title-prefix "Ford E-350 1991 V8-460 7.5L" \\
        --replace
"""
import argparse
import hashlib
import html
import json
import mimetypes
import os
import re
import shutil
import sys
import tempfile
import urllib.error
import urllib.request
import uuid
import zipfile
from collections import defaultdict

MIN_TEXT_CHARS = 400  # below this (and no image), a page isn't worth a chunk
MAIN_RE = re.compile(r"<div class='main'>(.*?)<div class=\"theme-colors footer\">", re.S)
H1_RE = re.compile(r"<h1>(.*?)</h1>", re.S)
IMG_RE = re.compile(r"""<img[^>]*class=['"]big-img['"][^>]*src=["']([^"']+)["']""")


def log(msg):
    print(msg, file=sys.stderr, flush=True)


def slugify(text):
    return re.sub(r"[^A-Za-z0-9]+", "-", text).strip("-").lower()


# ---------------------------------------------------------------- fetching

def obtain_section_dir(args, work_dir):
    """Returns the path to the manual section to walk (default: "Repair and
    Diagnosis" inside the extracted bundle)."""
    if args.extracted_dir:
        root = args.extracted_dir
    else:
        zip_path = args.bundle_zip
        if args.bundle_url:
            zip_path = os.path.join(work_dir, "bundle.zip")
            log(f"Downloading {args.bundle_url}")
            req = urllib.request.Request(args.bundle_url, headers={"User-Agent": "bosun-manual-importer/1.0"})
            with urllib.request.urlopen(req) as resp, open(zip_path, "wb") as out:
                shutil.copyfileobj(resp, out)
        if not zip_path:
            raise SystemExit("one of --extracted-dir, --bundle-zip, or --bundle-url is required")
        extract_dir = os.path.join(work_dir, "extracted")
        log(f"Extracting {zip_path}")
        with zipfile.ZipFile(zip_path) as zf:
            zf.extractall(extract_dir)
        top_level = [e for e in os.listdir(extract_dir) if os.path.isdir(os.path.join(extract_dir, e))]
        if len(top_level) != 1:
            raise SystemExit(f"expected exactly one top-level directory in the bundle, found {top_level}")
        root = os.path.join(extract_dir, top_level[0])

    if not args.section:
        return root
    # The bundle's directory names are literally percent-encoded on disk
    # (a quirk of how charm.li zips them) — encode the requested section
    # name to match before falling back to a plain match.
    candidates = [args.section, urllib.request.quote(args.section, safe="")]
    for name in candidates:
        candidate = os.path.join(root, name)
        if os.path.isdir(candidate):
            return candidate
    raise SystemExit(f"section {args.section!r} not found under {root} (tried {candidates})")


# ---------------------------------------------------------------- harvesting

def clean_text(fragment):
    text = re.sub(r"<[^>]+>", " ", fragment)
    text = html.unescape(text)
    return re.sub(r"\s+", " ", text).strip()


def decode_percent_path(path):
    return (
        path.replace("%20", " ")
        .replace("%2C", ",")
        .replace("%2F", "/")
        .replace("%28", "(")
        .replace("%29", ")")
    )


def harvest(section_dir, min_text_chars):
    """Walks section_dir and returns (text_pages_by_chapter, image_pages_by_chapter, image_path_by_hash).

    text_pages_by_chapter: {chapter: [(title, text), ...]}, deduped by text content hash.
    image_pages_by_chapter: {chapter: [{"caption": str, "hash": str}, ...]}, deduped by
      (caption, image hash).
    image_path_by_hash: {hash: absolute image path}, one canonical path per unique image.
    """
    raw_text_pages = []  # (path_len, chapter, title, text)
    raw_image_pages = []  # (path_len, chapter, caption, image_abs)

    for root, _dirs, files in os.walk(section_dir):
        for fn in files:
            if not fn.endswith(".html"):
                continue
            path = os.path.join(root, fn)
            with open(path, encoding="utf-8", errors="ignore") as f:
                content = f.read()
            m = MAIN_RE.search(content)
            if not m:
                continue
            main = m.group(1)
            h1 = H1_RE.search(main)
            title = clean_text(h1.group(1)) if h1 else fn
            rel = decode_percent_path(os.path.relpath(path, section_dir))
            parts = rel.split(os.sep)
            chapter = parts[0]
            subpath = os.sep.join(parts[1:-1])

            imgs = IMG_RE.findall(main)
            if imgs:
                image_abs = os.path.normpath(os.path.join(root, imgs[0]))
                if os.path.exists(image_abs):
                    caption = f"{chapter}: {title}" if not subpath else f"{chapter} > {subpath.replace(os.sep, ' > ')}: {title}"
                    raw_image_pages.append((len(path), chapter, caption, image_abs))
                    continue  # an image page is never also chunked as text

            text_only = clean_text(main)
            if len(text_only) >= min_text_chars:
                raw_text_pages.append((len(path), chapter, title, text_only))

    # Dedup text pages by content hash, preferring the shortest (most
    # canonical) source path — CHARM cross-lists the same content under
    # multiple category trees.
    raw_text_pages.sort(key=lambda e: e[0])
    seen_text_hashes = set()
    text_pages_by_chapter = defaultdict(list)
    for _plen, chapter, title, text in raw_text_pages:
        h = hashlib.sha256(text.encode()).hexdigest()
        if h in seen_text_hashes:
            continue
        seen_text_hashes.add(h)
        text_pages_by_chapter[chapter].append((title, text))

    # Dedup image pages by (caption, image hash) globally; this also caches
    # each unique image's content hash so the upload step below only
    # uploads (and the server only OCRs) each distinct image once, keyed
    # by its first-seen caption — CHARM cross-lists the same diagram under
    # multiple category trees with different captions, and uploading (and
    # re-OCRing) the identical bytes under every one of those would be
    # pure waste.
    raw_image_pages.sort(key=lambda e: e[0])
    image_hash_cache = {}

    def image_hash(path):
        if path not in image_hash_cache:
            with open(path, "rb") as f:
                image_hash_cache[path] = hashlib.sha256(f.read()).hexdigest()[:16]
        return image_hash_cache[path]

    seen_pages = set()
    image_pages_by_chapter = defaultdict(list)
    image_path_by_hash = {}
    for _plen, chapter, caption, image_abs in raw_image_pages:
        h = image_hash(image_abs)
        key = (caption, h)
        if key in seen_pages:
            continue
        seen_pages.add(key)
        image_path_by_hash.setdefault(h, image_abs)
        image_pages_by_chapter[chapter].append({"caption": caption, "hash": h})

    return text_pages_by_chapter, image_pages_by_chapter, image_path_by_hash


# ---------------------------------------------------------------- Bosun API

def api_get(bosun_url, path):
    with urllib.request.urlopen(f"{bosun_url}{path}") as resp:
        return json.load(resp)


def api_delete(bosun_url, doc_id):
    req = urllib.request.Request(f"{bosun_url}/api/documents/{doc_id}", method="DELETE")
    with urllib.request.urlopen(req) as resp:
        return json.load(resp)


def delete_existing_by_title_prefix(bosun_url, prefix):
    """Deletes every existing document whose title starts with
    "{prefix} — " — run once before a --replace import rather than
    matching per-chapter, since routing chapters through file dump
    changes a diagram chapter's title scheme from one document per
    chapter to one per image (see upload_file's docstring), so the old
    per-chapter titles this used to match by exact string no longer
    correspond to what this run will (re)create."""
    needle = f"{prefix} — "
    data = api_get(bosun_url, "/api/documents")
    deleted = 0
    for doc in data.get("documents", []):
        if doc["title"].startswith(needle):
            api_delete(bosun_url, doc["id"])
            deleted += 1
    log(f"  --replace: deleted {deleted} existing document(s) matching {prefix!r}")


def upload_file(bosun_url, folder, title, filename, content_bytes, ocr_langs):
    """POSTs one file into the file dump (POST /api/files/upload,
    docs/filedump.md) with add_to_rag=true — a plain-text chapter is
    chunked normally; an image is OCR'd server-side and stored as a
    single-page document (internal/webui/pdf.go's
    ingestStandaloneImage). Either way the result is tagged with `folder`
    as its SourcePath, which is what lets many chapters/images from one
    manual collapse into a single topic (see docs/settings.md's dynamic
    topics line) instead of one per chapter."""
    boundary = uuid.uuid4().hex

    def field(name, value):
        return f"--{boundary}\r\nContent-Disposition: form-data; name=\"{name}\"\r\n\r\n{value}\r\n".encode("utf-8")

    body = b"".join([
        field("path", folder),
        field("add_to_rag", "true"),
        field("title", title),
        field("ocr_language", ocr_langs),
    ])
    body += (
        f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"{filename}\"\r\n"
        f"Content-Type: {mimetypes.guess_type(filename)[0] or 'application/octet-stream'}\r\n\r\n"
    ).encode("utf-8")
    body += content_bytes
    body += f"\r\n--{boundary}--\r\n".encode("utf-8")

    req = urllib.request.Request(
        f"{bosun_url}/api/files/upload", data=body, method="POST",
        headers={"Content-Type": f"multipart/form-data; boundary={boundary}"},
    )
    with urllib.request.urlopen(req, timeout=590) as resp:
        return json.load(resp)


# ---------------------------------------------------------------- main

def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument("--bundle-url", help="URL to a CHARM 'long-names' bundle zip")
    source.add_argument("--bundle-zip", help="Path to an already-downloaded bundle zip")
    source.add_argument("--extracted-dir", help="Path to an already-extracted bundle directory")
    parser.add_argument("--section", default="Repair and Diagnosis",
                         help="Subdirectory to import (default: 'Repair and Diagnosis'; pass '' for the whole bundle)")
    parser.add_argument("--bosun-url", default="http://localhost:8080", help="Bosun base URL")
    parser.add_argument("--title-prefix", required=True, help='e.g. "Ford E-350 1991 V8-460 7.5L"')
    parser.add_argument("--min-text-chars", type=int, default=MIN_TEXT_CHARS)
    parser.add_argument("--ocr-langs", default="eng", help="tesseract -l value the server should use per image (docs/filedump.md)")
    parser.add_argument("--work-dir", help="Directory for downloads/extraction (default: a temp dir)")
    parser.add_argument("--skip-images", action="store_true", help="Only import text chapters, skip diagrams entirely")
    parser.add_argument("--replace", action="store_true",
                         help="Delete every existing document titled '{title-prefix} — ...' before importing")
    args = parser.parse_args()

    work_dir = args.work_dir or tempfile.mkdtemp(prefix="bosun-manual-import-")
    os.makedirs(work_dir, exist_ok=True)
    log(f"Working directory: {work_dir}")

    if args.replace:
        delete_existing_by_title_prefix(args.bosun_url, args.title_prefix)

    section_dir = obtain_section_dir(args, work_dir)
    log(f"Importing from: {section_dir}")

    text_pages, image_pages, image_path_by_hash = harvest(section_dir, args.min_text_chars)
    log(f"Found {sum(len(v) for v in text_pages.values())} text pages across {len(text_pages)} chapters")
    log(f"Found {sum(len(v) for v in image_pages.values())} image pages "
        f"({len(image_path_by_hash)} unique images) across {len(image_pages)} chapters")

    slug = slugify(args.title_prefix)

    for chapter, pages in sorted(text_pages.items()):
        title = f"{args.title_prefix} — {chapter}"
        blob = "\n\n".join(f"## {t}\n\n{text}" for t, text in sorted(pages))
        log(f"Uploading text chapter: {title} ({len(pages)} pages, {len(blob)} chars)")
        result = upload_file(args.bosun_url, slug, title, "manual.txt", blob.encode("utf-8"), args.ocr_langs)
        log(f"  -> {result}")

    if args.skip_images or not image_pages:
        log("Done.")
        return

    # One representative caption per unique image (first one harvest()
    # encountered) — see upload_file's docstring on why this is per unique
    # image, not per (chapter, caption) page entry.
    caption_by_hash = {}
    for pages in image_pages.values():
        for p in pages:
            caption_by_hash.setdefault(p["hash"], p["caption"])

    diagrams_folder = f"{slug}/diagrams"
    total = len(image_path_by_hash)
    for i, (h, src) in enumerate(sorted(image_path_by_hash.items())):
        caption = caption_by_hash[h]
        ext = os.path.splitext(src)[1] or ".png"
        with open(src, "rb") as f:
            content = f.read()
        log(f"Uploading diagram {i + 1}/{total}: {caption}")
        result = upload_file(args.bosun_url, diagrams_folder, caption, f"{h}{ext}", content, args.ocr_langs)
        log(f"  -> {result}")

    log("Done.")


if __name__ == "__main__":
    try:
        main()
    except urllib.error.HTTPError as exc:
        log(f"HTTP error: {exc.code} {exc.read().decode(errors='replace')}")
        sys.exit(1)

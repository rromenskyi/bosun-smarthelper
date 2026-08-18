#!/usr/bin/env python3
"""Import a CHARM-style ("Operation CHARM" / charm.li) car service manual
bundle into Bosun's document knowledge base (see docs/memo-search.md).

This is the exact pipeline used to load the Ford E-350 1991 V8-460 7.5L
manual: one document per manual chapter, real text chunked normally,
diagram-only pages (fuse panels, wiring diagrams) rendered as images with
OCR'd text attached so they're still findable by content, not just a
generic caption.

See README.md in this directory for prerequisites and a full walkthrough.

Example:
    python3 import_manual.py \\
        --bundle-url "https://charm.li/bundle/long-names/Ford/1991/E%20350%20Van%20V8-460%207.5L/" \\
        --bosun-url http://localhost:8080 \\
        --container bosun \\
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
import subprocess
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

    # Dedup image pages by (caption, image hash) globally; cache each
    # unique image's hash so it's only OCR'd once regardless of how many
    # pages/chapters reference it.
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


# ---------------------------------------------------------------- OCR

def run_ocr(image_path_by_hash, cache_path, langs):
    """OCRs every unique image, resuming from cache_path if it already has
    some entries (e.g. a prior interrupted run)."""
    results = {}
    if os.path.exists(cache_path):
        with open(cache_path, encoding="utf-8") as f:
            results = json.load(f)

    todo = [h for h in image_path_by_hash if h not in results]
    log(f"OCR: {len(image_path_by_hash) - len(todo)} cached, {len(todo)} to process")
    for i, h in enumerate(todo):
        path = image_path_by_hash[h]
        try:
            out = subprocess.run(
                ["tesseract", path, "-", "-l", langs],
                capture_output=True, text=True, timeout=60,
            )
            results[h] = out.stdout.strip()
        except Exception as exc:  # noqa: BLE001 - best-effort, never abort the batch
            log(f"  OCR failed for {path}: {exc}")
            results[h] = ""
        if (i + 1) % 25 == 0 or (i + 1) == len(todo):
            with open(cache_path, "w", encoding="utf-8") as f:
                json.dump(results, f)
            log(f"  OCR {i + 1}/{len(todo)}")
    with open(cache_path, "w", encoding="utf-8") as f:
        json.dump(results, f)
    return results


# ---------------------------------------------------------------- Bosun API

def api_get(bosun_url, path):
    with urllib.request.urlopen(f"{bosun_url}{path}") as resp:
        return json.load(resp)


def api_delete(bosun_url, doc_id):
    req = urllib.request.Request(f"{bosun_url}/api/documents/{doc_id}", method="DELETE")
    with urllib.request.urlopen(req) as resp:
        return json.load(resp)


def delete_existing_by_title(bosun_url, title):
    data = api_get(bosun_url, "/api/documents")
    for doc in data.get("documents", []):
        if doc["title"] == title:
            log(f"  --replace: deleting existing document {doc['id']} ({title})")
            api_delete(bosun_url, doc["id"])


def upload_text_document(bosun_url, title, text, replace):
    if replace:
        delete_existing_by_title(bosun_url, title)
    boundary = uuid.uuid4().hex
    body = []
    body.append(f"--{boundary}\r\nContent-Disposition: form-data; name=\"title\"\r\n\r\n{title}\r\n")
    body.append(f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"manual.txt\"\r\nContent-Type: text/plain\r\n\r\n")
    payload = "".join(body).encode("utf-8") + text.encode("utf-8") + f"\r\n--{boundary}--\r\n".encode("utf-8")
    req = urllib.request.Request(
        f"{bosun_url}/api/documents", data=payload, method="POST",
        headers={"Content-Type": f"multipart/form-data; boundary={boundary}"},
    )
    with urllib.request.urlopen(req, timeout=590) as resp:
        return json.load(resp)


def upload_pages_document(bosun_url, title, pages, replace):
    if replace:
        delete_existing_by_title(bosun_url, title)
    payload = json.dumps({"title": title, "pages": pages}).encode("utf-8")
    req = urllib.request.Request(
        f"{bosun_url}/api/documents/pages", data=payload, method="POST",
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=590) as resp:
        return json.load(resp)


def copy_images_into_container(docker_bin, container, staging_dir, image_names):
    """docker cp's just-referenced image files into the container's
    document-images directory, then fixes ownership (docker cp as root
    leaves files the container's non-root user can't otherwise chown)."""
    if not image_names:
        return
    subprocess.run([*docker_bin, "exec", container, "mkdir", "-p", "/home/bosun/.local/share/bosun/document-images"], check=True)
    for name in image_names:
        subprocess.run(
            [*docker_bin, "cp", os.path.join(staging_dir, name), f"{container}:/home/bosun/.local/share/bosun/document-images/{name}"],
            check=True,
        )
    subprocess.run(
        [*docker_bin, "exec", "-u", "root", container, "chown", "-R", "bosun:bosun", "/home/bosun/.local/share/bosun/document-images"],
        check=True,
    )


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
    parser.add_argument("--container", default="bosun", help="Docker container name running Bosun (for image copy)")
    parser.add_argument("--docker-sudo", action="store_true", help="Prefix docker commands with 'sudo -n'")
    parser.add_argument("--title-prefix", required=True, help='e.g. "Ford E-350 1991 V8-460 7.5L"')
    parser.add_argument("--min-text-chars", type=int, default=MIN_TEXT_CHARS)
    parser.add_argument("--ocr-langs", default="eng+rus", help="tesseract -l value")
    parser.add_argument("--work-dir", help="Directory for downloads/extraction/OCR cache (default: a temp dir)")
    parser.add_argument("--skip-images", action="store_true", help="Only import text chapters, skip diagrams/OCR entirely")
    parser.add_argument("--replace", action="store_true", help="Delete an existing document with the same title before creating it")
    args = parser.parse_args()

    docker_bin = ["sudo", "-n", "docker"] if args.docker_sudo else ["docker"]

    work_dir = args.work_dir or tempfile.mkdtemp(prefix="bosun-manual-import-")
    os.makedirs(work_dir, exist_ok=True)
    log(f"Working directory: {work_dir}")

    section_dir = obtain_section_dir(args, work_dir)
    log(f"Importing from: {section_dir}")

    text_pages, image_pages, image_path_by_hash = harvest(section_dir, args.min_text_chars)
    log(f"Found {sum(len(v) for v in text_pages.values())} text pages across {len(text_pages)} chapters")
    log(f"Found {sum(len(v) for v in image_pages.values())} image pages "
        f"({len(image_path_by_hash)} unique images) across {len(image_pages)} chapters")

    for chapter, pages in sorted(text_pages.items()):
        title = f"{args.title_prefix} — {chapter}"
        blob = "\n\n".join(f"## {t}\n\n{text}" for t, text in sorted(pages))
        log(f"Uploading text document: {title} ({len(pages)} pages, {len(blob)} chars)")
        result = upload_text_document(args.bosun_url, title, blob, args.replace)
        log(f"  -> {result}")

    if args.skip_images or not image_pages:
        return

    ocr_cache_path = os.path.join(work_dir, "ocr-cache.json")
    ocr_by_hash = run_ocr(image_path_by_hash, ocr_cache_path, args.ocr_langs)

    staging_dir = os.path.join(work_dir, "images-staging")
    os.makedirs(staging_dir, exist_ok=True)
    image_name_by_hash = {}
    for h, src in image_path_by_hash.items():
        ext = os.path.splitext(src)[1] or ".png"
        name = f"{re.sub(r'[^A-Za-z0-9]+', '-', args.title_prefix).strip('-').lower()}-{h}{ext}"
        image_name_by_hash[h] = name
        dst = os.path.join(staging_dir, name)
        if not os.path.exists(dst):
            shutil.copyfile(src, dst)

    log(f"Copying {len(image_name_by_hash)} unique images into container {args.container!r}")
    copy_images_into_container(docker_bin, args.container, staging_dir, list(image_name_by_hash.values()))

    for chapter, pages in sorted(image_pages.items()):
        title = f"{args.title_prefix} — {chapter} (Diagrams)"
        api_pages = []
        for p in pages:
            ocr_text = ocr_by_hash.get(p["hash"], "")
            text = p["caption"] if not ocr_text else f"{p['caption']}\n\n{ocr_text}"
            api_pages.append({"text": text, "image_url": f"/document-images/{image_name_by_hash[p['hash']]}"})
        log(f"Uploading pages document: {title} ({len(api_pages)} pages)")
        result = upload_pages_document(args.bosun_url, title, api_pages, args.replace)
        log(f"  -> {result}")

    log("Done.")


if __name__ == "__main__":
    try:
        main()
    except urllib.error.HTTPError as exc:
        log(f"HTTP error: {exc.code} {exc.read().decode(errors='replace')}")
        sys.exit(1)

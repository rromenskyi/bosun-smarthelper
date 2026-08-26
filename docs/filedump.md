# File dump: raw file storage + optional RAG ingestion

A 📄 button in the web UI for a general-purpose file tree — real
folders, drag-and-drop upload and reorganize, any file type, stored
as-is. It replaces the old flat "Documents" upload dialog: uploading is
now one surface (`internal/webui/static/filedump.js`), with a per-upload
checkbox to *also* feed a file into the existing RAG document search
store (`internal/documents`, `docs/memo-search.md`) rather than two
separate upload flows.

## Why a separate package from `internal/documents`

`internal/documents` only ever holds *embedded text chunks* — there is
no way to get the original file back out of it, and it has no notion of
folders (a flat `map[string]Record` keyed by generated ID). Neither fits
"store a car manual, a receipt photo, and a spreadsheet as themselves,
organized into folders a person can browse." `internal/filedump` is a
plain filesystem mirror instead: listing, creating a folder,
moving/renaming, and recursively deleting are just
`os.ReadDir`/`os.MkdirAll`/`os.Rename`/`os.RemoveAll` under one root
directory — no separate index to keep in sync with the tree structure
itself, since the filesystem already *is* that index for free.

## RAG association: a sidecar link, not a merged store

A file is either fed into RAG or not, as a whole — no per-chunk
override for this feature. `internal/filedump.Store` keeps a small JSON
sidecar, `filedump-index.json` (a sibling *file* next to the tree's root
*directory*, so it never itself shows up as a browsable entry), mapping
a file's tree-relative path to the `documents.Record` ID it produced.
Only `internal/filedump` reads or writes this file —
`internal/documents` has no idea the sidecar exists; it just gets an
extra `sourcePath` argument on `Add`/`AddPages`, stored as
`Record.SourcePath` and surfaced in `search` results as `source_path`
(see `docs/memo-search.md`).

Moving or renaming a folder can carry many RAG-linked files with it at
once. `Store.Move` walks the sidecar for every entry whose path has the
old path as a prefix, rewrites it to the new prefix, and returns what
changed (`[]LinkUpdate`); `internal/webui/filedump.go`'s
`handleFileDumpMove` then calls `documents.Store.UpdateSourcePath` for
each, so a `search` result's `source_path` always reflects where the
file currently lives, not where it was uploaded from originally.
Deleting a file or a (recursively) non-empty folder works the same way:
`Store.Delete` returns every `documents.Record` ID that was linked
under what got removed, and the handler cascades
`documents.Store.Delete` for each.

Automatic **chunk-level** tagging/post-processing (attaching a topic tag
to individual RAG chunks, distinct from a whole file's folder-level
`source_path`) is deliberately out of scope here — a possible future
addition on top of `internal/documents`, not something this feature
does.

## Storage layout

```
<filedump.path>/                  # the browsable tree itself — served
  docs/                           # read-only via GET /files/... (raw
    ford/                         # bytes, no processing)
      generator-repair.pdf
  receipts/
    2026-08-fuel.jpg

<dirname(filedump.path)>/filedump-index.json   # sidecar RAG-link index
```

Path safety is centralized in one function
(`internal/filedump/path.go`'s `safeJoin`) that every operation routes
through — clean any user-supplied relative path, then verify the
resolved absolute path still falls under the configured root before
touching the filesystem, rather than each handler re-deriving its own
`..`-rejection.

## Upload: streamed to disk, no small hard cap

Unlike the old document-upload path's `maxDocumentUploadBytes` (2MB,
buffered via `ParseMultipartForm`), `POST /api/files/upload` reads the
request with `r.MultipartReader()` and streams the `file` part straight
to disk with `io.Copy` — nothing beyond one chunk buffer ever sits in
memory regardless of file size. There's still a generous backstop
(`fileDumpUploadHardLimit`, 4GB) via `http.MaxBytesReader`, purely
against a runaway request — the *real* limit is a client-side `confirm()`
above a size threshold in `filedump.js`, since a personal file store has
no reason to hard-reject a large upload a user actually intends to make.

When `add_to_rag=true`, the file is read back after the raw write
completes and run through the same PDF/plain-text extraction the old
upload path used (`internal/webui/pdf.go`'s `extractPDFPages`, or plain
UTF-8 text), tagged with the file's folder as `SourcePath`. A failed
ingestion (not a PDF, not valid UTF-8, extraction error) **never rolls
back the raw file write** — the file is still saved, and the failure
comes back as a non-fatal `rag_warning` in the response instead of an
error. Uploading a photo or a spreadsheet with the checkbox mistakenly
checked is expected to happen; it shouldn't lose the file.

## API

- `GET /api/files?path=<relpath>` — list one folder's contents
  (`{folders: [{name}], files: [{name, size, mtime, in_rag, document_id}]}`).
  `enabled: false` (no folders/files) when `filedump.path` is unset.
- `POST /api/files/folder` `{path, name}` — create a subfolder.
- `POST /api/files/upload` — multipart form: `path` (target folder),
  `file`, and optionally `add_to_rag` (`"true"`), `title`,
  `ocr_language` (same tesseract language codes as
  `docs/memo-search.md`'s OCR section). Metadata fields must arrive
  before the `file` part in the multipart stream — true for a `FormData`
  built by appending fields in that order, which is what `filedump.js`
  does; a streaming reader can't look ahead to find them otherwise.
- `POST /api/files/move` `{from, to}` — move or rename a file or folder;
  also used by the UI's drag-and-drop.
- `DELETE /api/files?path=<relpath>&recursive=true` — delete a file, or
  (with `recursive=true`) a folder and everything inside it, cascading
  to any linked `documents.Record`s.
- `GET /files/<relpath>` — raw byte download, a plain
  `http.FileServer` over the tree root (only registered when
  `filedump.path` is set).

## Config

```yaml
filedump:
  path: ""  # empty (the default) disables the feature entirely — no
            # /api/files routes do anything, GET /files/ isn't
            # registered, and the 📄 button hides itself the same way
            # the 📊/📹 buttons do when their own feature is off
```

Unlike `documents.path` (which always resolves to a default location
since document search is effectively always on), there's no default
here — a raw, browsable file tree is opt-in, not something to create
unasked.

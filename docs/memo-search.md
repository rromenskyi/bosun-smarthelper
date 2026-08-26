# Memo and document semantic search

`memo`'s `search` action finds memos — and, once a document store is wired
in, uploaded reference documents (manuals, how-tos) — by meaning, not just
exact words. "What did I say about the fridge?" matches a memo about a
faulty compressor even if it never uses the word "fridge."

## Why two separate stores

- **Memos** (`internal/tools/memo.go`): short personal notes, one embedding
  per whole note. Written, read, listed, archived, and deleted through the
  `memo` tool — the model can create these itself.
- **Documents** (`internal/documents`): long reference text. Too long to
  embed as one vector without losing precision, so it's split into
  paragraph/sentence-bounded chunks (`chunkText` in
  `internal/documents/chunker.go`) and each chunk gets its own embedding.
  Uploaded **only through the web UI** — the file dump's upload endpoint
  (`POST /api/files/upload`, plain-text or PDF, with `add_to_rag=true` —
  see `docs/filedump.md`) — deliberately **not** an LLM-callable tool
  action. A weak local model already struggles with a handful of tool
  definitions (see `docs/token-budget.md`); adding upload/list/delete as
  tool actions would grow the contract for a capability only a human ever
  needs (nobody asks the assistant out loud to ingest a file). Search is
  the only document capability exposed to the model, and it reuses the
  existing `memo` tool's `search` action rather than registering a second
  tool — so the contract doesn't grow at all.

  Every `Record` also carries a `SourcePath` — the file dump tree folder
  it was uploaded from (e.g. `docs/ford/generator-repair`), empty for
  anything uploaded before that feature existed or added through
  `POST /api/documents/pages`'s scripted path. A document `search` result
  includes it as `source_path` when non-empty, so the model can tell two
  similarly-described pieces of equipment apart by where their manual
  came from (a Ford's generator vs. a generic 150cc engine's) instead of
  just returning "the manual" with no origin. See `docs/filedump.md` for
  how a file's folder becomes its documents' `SourcePath`, including what
  happens to it when the file is later moved.

## Images: diagrams, with OCR where possible

Some reference material — a fuse panel chart, a wiring diagram — is a
picture, not text. There's no vision model here (nothing "looks at" the
image at answer time), but a `Chunk` can carry an `ImageURL` alongside
text recognized from it by OCR (`tesseract`; see "PDF ingestion" below
and `examples/import-manual/`), so it's still findable by actual content,
not just a generic caption. OCR quality varies with scan/render quality
and isn't guaranteed to find anything; when it doesn't, the page's
title/caption is what's left to search against. A `search` result with an
image includes `image_url`; the memo tool's description tells the model
to drop it into its answer as a normal markdown image
(`![description](image_url)`), and the web UI already renders that
inline — see `renderMessageHTML` in `index.html`. Images are served from
`internal/documents.Store.ImagesDir()` (a sibling directory to
`documents.json`) via `GET /document-images/...` in `server.go`.

OCR language defaults to `eng` (an upload can set a different one via the
`ocr_language` form field — see `internal/webui/pdf.go`'s
`defaultOCRLanguage`), not `eng+rus`: running both on an English-only
technical diagram measurably made things worse, not more permissive —
tesseract's combined model frequently misread plain English glyphs as
look-alike Cyrillic ones. `internal/documents.CleanOCRText` also strips
residual OCR noise (stray single characters, symbol-embedded garbage, any
token with a non-ASCII letter) from a chunk's OCR body at ingestion time,
leaving its structured head (page number or breadcrumb) untouched.

### Attaching images to the text that actually covers them

A batch of scanned diagram pages and a batch of the same manual's
procedural text are often uploaded as separate documents (a scraper's own
choice, not something this project controls) — without linking them, an
image chunk is just a weakly-captioned orphan competing in the same search
pool as real prose, and a hit on one never surfaces the other even when
they cover the exact same topic. `internal/documents.Store.AttachOrphanedImages`
fixes this after the fact: for every image chunk, it finds the
best-matching text chunk *anywhere in the store* by cosine similarity
between their existing embeddings, and — above a relevance floor — merges
the image onto that text chunk and drops the now-redundant standalone
one. Matching is purely by embedding similarity, not by document title or
any "(Diagrams)"-style naming convention, so it's not tied to how this
particular corpus happened to be produced. Run it (or re-run it after a
new batch of uploads) via `smarthelper documents attach-images`; an image
with no good match is left standalone, still searchable on its own OCR'd
text exactly as before. Merging also removes any document left with zero
chunks as a result (this run's or an earlier one's) — an empty document
record is pure clutter, never contributing a search result.

The merge threshold is `memo.attach_image_min_relevance` (default 0.6),
deliberately higher than `min_search_relevance` (0.4): showing a so-so
search result is low-stakes, but permanently merging an image onto the
wrong chunk isn't safe at the same bar. Confirmed live at the default
0.4 — an image from an unrelated Ford manual merged onto an unrelated
Valvoline product sheet's chunk at 0.41, a false positive across two
genuinely unrelated documents (as opposed to, say, two different
subsystems of the same manual, where a wrong-but-nearby match is lower
stakes). There's no way to undo an already-made merge automatically —
the original orphaned chunk's text is gone once merged — so a wrong one
found after the fact has to be fixed by hand (clear that chunk's
`image_url`) or by re-uploading the affected document.

## PDF ingestion

`POST /api/files/upload` with `add_to_rag=true` also accepts a PDF
(detected by its `%PDF-` magic bytes). `internal/webui/pdf.go` shells out
to poppler-utils
(`pdfinfo`/`pdftotext`/`pdftoppm` — installed in the Docker image, see the
Dockerfile) page by page: a page with a real text layer becomes a text
`PageInput`; a page below `minPDFPageTextChars` (a scanned page, or one
that's mostly a diagram) is instead rendered to a PNG and becomes an
image-only `PageInput`, same as the manually-curated diagrams above.
**There is no OCR** — a scanned page's rendered image has no extracted
text next to it, so it's only findable by its generic "Page N" label, not
by its actual content. Adding an OCR engine (e.g. `tesseract-ocr`) would
close that gap for scanned manuals; it isn't installed yet.

`POST /api/documents/pages` is a separate, script-only ingestion path
(JSON body: `{"title", "pages": [{"text", "image_url"}]}`, no UI button)
for a bulk import that already has its own pre-segmented pages and image
files — e.g. an HTML-based manual site's pages, where the importer script
copies image files directly into `ImagesDir()` and references them by the
resulting `/document-images/...` path. `examples/import-manual/` is the
reusable version of the pipeline that loaded the CHARM Ford E-350 service
manual this way: one document per manual chapter, text pages chunked
normally, diagram pages (e.g. the fuse panel charts under Power and
Ground Distribution) OCR'd and added with their original image plus a
caption as text — see that directory's README for prerequisites and how
to point it at a different manual.

## Chunking

Paragraphs (blank-line-separated) already under ~1500 characters are kept
whole, so real paragraph boundaries survive untouched. Only an oversized
paragraph is split further, at sentence boundaries — greedily packing whole
sentences until the next one would exceed the limit — so a chunk never cuts
a sentence in half. This means chunks are uneven in size by design; that
trade-off was chosen deliberately over fixed-size windows, which are
simpler but can slice a sentence in two.

## Ranking and graceful degradation

Both stores use the same `internal/embeddings.Client` (an OpenAI-compatible
`/embeddings` call) and `embeddings.CosineSimilarity`. `search` merges
memo and document hits into one relevance-sorted list, tagged
`"source": "memo"` or `"source": "document"`.

Nothing about this feature can turn a working memo/document write into a
failure:

- `llm.embeddings.base_url` empty (the default) — no embeddings are
  computed at all; `search` falls back to a plain substring match instead
  of erroring.
- Embeddings configured but the server is unreachable when writing a memo
  or uploading a document — the write still succeeds, just without a
  vector; that item won't be found by semantic search until re-saved, but
  substring search still finds it.
- Embeddings configured but unreachable at *search* time — same substring
  fallback, per store, independently.

Two more guards on what actually reaches the LLM, added after a real
incident (a weak local model, fed a handful of marginal, OCR-garbled
document matches, degenerated into repeating a single token hundreds of
times):

- **`memo.min_search_relevance`** (default `0.4`) drops any hit — memo or
  document — below that cosine similarity before it's returned at all. A
  substring-fallback match (embeddings unreachable) always scores `1` and
  is never filtered, since it's already a real match by definition, not a
  similarity guess.
- **Each result's text is capped** at `maxSearchResultChars` (500 runes,
  `internal/tools/memo.go`) — a search result should point at the answer,
  not paste the whole source; unbounded text here (up to 5 results × a
  1500-char document chunk, or memo's own 10000-char write limit) is
  itself a plausible trigger for a weak model choking on context.

A third, complementary guard lives in `internal/agent` — see
`repetitionDetector`: even with these two in place, any model can still
degenerate on its own, so the agent loop watches its own streamed output
for a runaway repeated substring and cuts the response off rather than
relaying it indefinitely.

## Tags: exact recall vs. similarity

Semantic search answers "find something like X," ranked by similarity —
great for a one-shot lookup, but it can't guarantee *every* matching memo
comes back, since a top-K cutoff can drop a real match that just happens
to be phrased differently. "Show me every purchase" needs exact recall,
which similarity ranking alone doesn't promise.

`write` accepts an optional `tags` array — the model adds a few short
topic tags in the same call that saves the content, so this costs nothing
beyond a few extra output tokens (no second LLM call). `list` and `search`
accept a `tag` filter that only considers memos carrying that exact tag
(case-insensitive, matched against both `tags` and `canonical_tags` below)
— that's the exact-recall path.

### Background normalization (optional)

Free-form tags drift: "бензонасос" today, "топливная система" next month,
never matching each other. If `memo.canonical_tags` is configured,
`internal/tools/memo_tags.go`'s `NormalizeTags` periodically batches
memos with unmapped free tags into **one** LLM call (not one call per
memo) asking it to map each onto that fixed vocabulary. A match is added
to `canonical_tags` — `tags` is never modified or replaced, so a free tag
is never destroyed, and both remain searchable via `tag`.

This only runs between chat turns, never during or right after one:
`cmd/smarthelper`'s `runTagNormalizer` ticks on
`memo.tag_normalize_interval` (default 5m), calling
`webui.Server.TryIdleAfter(interval, ...)`, which requires *both* that no
chat request is currently in flight (the same single-in-flight-request
slot a real chat request claims) *and* that at least `interval` has
passed since the last one finished. The second condition matters on its
own: without it, a tick landing moments after a chat just ended would
immediately grab the slot for its own duration, so a user typing a
follow-up right then would queue behind background maintenance instead of
getting an instant reply. Leaving `canonical_tags` empty (the default)
disables the whole pass — no ticker, no LLM calls, `tag` filters still
work against whatever free-form tags already exist.

## Why a second local llama-server instance

Neither this deployment's remote proxy (`405 Method Not Allowed` on
`/v1/embeddings`) nor the local chat model (`llama-server` needs
`--embeddings` at startup, and a chat-tuned model isn't ideal for
embeddings anyway) can serve embeddings. `llama-embed` in
`docker-compose.yml` runs a small multilingual embedding model
(`nomic-ai/nomic-embed-text-v2-moe-GGUF`, chosen for solid Russian support
since that's this deployment's primary language) dedicated to this — see
`docs/docker.md`.

Unlike `llama-chat`, embeddings are only needed for occasional memo/
document writes and searches, not every chat turn, so `llama-embed` runs
with `--sleep-idle-seconds 300`: llama.cpp fully unloads the model (frees
~500MB RSS, confirmed by hand) after 5 minutes with no request, and
transparently reloads it on the next one (~2s, imperceptible for a
background operation). This is a genuine unload/reload, not a pause —
see `handle_sleeping_state` in llama.cpp's `server-context.cpp`.

## Config

```yaml
llm:
  embeddings:
    base_url: "http://localhost:1235/v1"  # empty disables the feature
    model: "embed"
    timeout: 10s

documents:
  path: ""  # empty uses ~/.local/share/bosun/documents.json

filedump:
  path: ""  # empty disables the file dump entirely — see docs/filedump.md

memo:
  path: ""
  canonical_tags: []  # empty disables background tag normalization
  tag_normalize_interval: 5m
  min_search_relevance: 0.4  # 0 (or below) disables relevance filtering
```

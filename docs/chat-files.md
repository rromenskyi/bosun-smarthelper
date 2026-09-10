# Chat file attachments

Three ways to attach a file directly to a message — a photo, a PDF, a
short text file — as scratch input for that conversation, distinct from
`filedump.md`'s permanent, browsable file tree:

- A 📎 button next to the compose bar (opens a file picker).
- Drag-and-drop anywhere on the page, not just the compose bar — the
  whole window is the drop target (with a page-wide `preventDefault` so
  an errant drop never navigates the browser away to the file instead).
- Paste (e.g. a screenshot copied from another app) directly into the
  message box. A pasted file has no real name, and browsers reuse the
  same generic one ("image.png") for every paste in a session, so it's
  renamed to something unique (`pasted-<timestamp>.<ext>`) before
  upload — otherwise a second pasted screenshot would silently overwrite
  the first one still waiting to be claimed (see chatfiles.Store.Save's
  same-name-replaces behavior).

Nothing happens to the file automatically: it's up to the model, via the
`chat_file` tool, once the user's message says what to do with it ("add
this to the fuse panel note", "add this to search as 'generator
manual'").

## Why a separate store from filedump

`internal/filedump` is a deliberate, permanent upload into a browsable
tree. A chat attachment is disposable: it exists only to answer "what do
I do with this file right now," and is gone within about an hour whether
or not anything happened to it. `internal/chatfiles` is a small,
independent package for exactly that — one temp subdirectory per chat
session, no persistence guarantees, cleaned up by a TTL reaper
(`chatfiles.Run`, same ticker-goroutine shape as `internal/sandbox`'s
reaper) rather than kept forever like filedump.

## No system-prompt changes

Unlike the dynamic-topics prompt line (`docs/settings.md`), attaching a
file adds nothing to the system prompt. Two things make the model aware
of it instead:

- The `chat_file` tool's own name and description — discovered the same
  way any tool is, through normal tool-calling reasoning.
- A short note the client appends to that one outgoing message's own
  text (e.g. `[Attached: photo.jpg]`) — not a standing instruction, just
  part of what the user said in this turn, the same as if they had typed
  "here's photo.jpg" themselves.

## The `chat_file` tool

Six actions, all scoped to the current chat session
(`tools.SessionIDFromContext` — the same mechanism `run_code` already
uses to scope a sandbox workspace per conversation):

- `list` — names and sizes of whatever's currently attached.
- `download_url` — fetches a file from a public http(s) URL (e.g. one a
  `web_search` call turned up) and attaches it exactly as if the user had
  pasted it in, so it's then usable with `add_to_rag`/`add_to_memo` like
  any other attachment — the bridge that makes "find the manual for X,
  add it to the manuals folder" work in one turn. `run_code`'s sandbox
  already has full network access, but a file it downloads is trapped in
  that sandbox's own workspace with no path out to `chat_file`/RAG —
  this fetches server-side instead, with nowhere for a file to get
  stuck. Every resolved IP (including on each redirect hop, up to 5) is
  checked against `checkPublicURL` and rejected if it's loopback,
  private, link-local, or unspecified — this server fetches whatever URL
  the model was told about, which could be echoed back from a page's own
  content (prompt injection) or just wrong, so without this a request
  could reach an internal service (this LAN's other devices, this host's
  own other ports) that was never meant to be reachable from a chat
  request. Capped at the same 25MB `internal/webui/chatfiles.go` upload
  endpoint uses (`maxDownloadBytes`) and a 30s total timeout
  (`downloadTimeout`); `filename` is optional, defaulting to the URL's
  own last path segment.
- `describe` — answers "what's in this photo" directly: a one-off
  vision request (the raw image as a base64 `image_url` content part,
  plus a text prompt — `prompt` is optional, defaulting to a general
  "describe this image" ask), bypassing the normal `[]Message`/
  string-content path every other call uses. Tries remote first
  (`RemoteClient.DescribeImage`, `internal/llm/vision.go`), falls back
  to local (`LocalClient.DescribeImage`, `internal/llm/local_vision.go`)
  only if that's exhausted:
  - Remote is fast when it lands on a working backend, but the proxy
    behind this deployment fans a single model name out across multiple
    upstream backends and not all of them support image input
    (confirmed live: one rejects any multimodal request outright) — a
    request landing on the wrong one fails, so `DescribeImage` retries
    several times before giving up, since a later attempt has a real
    chance of landing on a vision-capable backend instead.
  - Local (llama-server serving Gemma 3n) turned out to already support
    vision out of the box — its GGUF repo ships an `mmproj` file that
    `-hf`'s `--mmproj-auto` (the llama-server default) downloads and
    loads automatically, at no cost to ordinary text turns: the
    projector only runs when a request actually carries an image. It's
    slow (a real photo measured ~170s end-to-end on this deployment's
    CPU-only hardware, once prompt processing and generation are both
    accounted for) but doesn't depend on someone else's infrastructure —
    worth the wait only as a last resort, after every remote attempt has
    already failed. Only works when local is configured with
    `api_format: openai` (this deployment's setup) — the native Ollama
    image-embedding shape isn't implemented.
  A response's leading `<think>...</think>` block (reasoning-model
  output) is stripped from either path, keeping only the actual answer.
- `read` — returns a small text file's content directly (txt/csv/
  markdown/json only, capped at ~200KB) so the model can discuss it or
  fold it into a memo itself.
- `add_to_rag` — ingests any file (photo, PDF, or text) into
  `internal/documents`, through the exact same extraction code a
  filedump upload uses (`documents.ExtractPDFPages`/
  `IngestStandaloneImage`/`SniffImageExt`/`IsPDF` — moved there from
  `internal/webui/pdf.go` specifically so this tool and filedump uploads
  share one implementation). Requires `title` (and optionally `folder`)
  — the tool's own description tells the model to ask the user rather
  than guess.
- `add_to_memo` — links the file to an existing memo via
  `MemoTool.AttachFile`, which writes it into `memos/<key>/` in the
  filedump tree and appends that path to the memo's `Attachments`. A
  memo's `read`/`search` results include each attachment as a
  `/files/<path>` URL; the model can embed one in its reply as
  `![...](url)` and the chat UI's existing markdown renderer turns that
  into an `<img>` — no new rendering code needed. Deleting a memo
  cascades to remove its attachment files too.

A file is deleted from chat storage as soon as `add_to_rag`/`add_to_memo`
claims it, rather than waiting for the TTL reaper — so it's never offered
twice by `list`.

## API

- `GET /api/chat/files?session_id=<id>` — list attachments for a
  session. `{enabled: false}` (no `files`) when no chat files store is
  configured.
- `POST /api/chat/files` — multipart form: `session_id` (must arrive
  before `file` in the stream, same constraint `handleFileDumpUpload`
  already documents), `file`. Responds `{name}` — the sanitized filename
  actually used (basename only; a name with directory components is
  reduced to its base).
- `DELETE /api/chat/files?session_id=<id>&name=<name>` — remove one
  attachment before sending, e.g. if the user picked the wrong file.

## Config

No configuration — always on, backed by a fixed subdirectory of the
host's temp directory (`os.TempDir()/bosun-chat-files`), not the
persistent data directory: nothing here needs backing up or surviving a
restart.

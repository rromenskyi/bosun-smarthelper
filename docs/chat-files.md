# Chat file attachments

A 📎 button next to the chat compose bar (plus drag-and-drop onto it) for
attaching a file directly to a message — a photo, a PDF, a short text
file — as scratch input for that conversation, distinct from
`filedump.md`'s permanent, browsable file tree. Nothing happens to the
file automatically: it's up to the model, via the `chat_file` tool, once
the user's message says what to do with it ("add this to the fuse panel
note", "add this to search as 'generator manual'").

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

Four actions, all scoped to the current chat session
(`tools.SessionIDFromContext` — the same mechanism `run_code` already
uses to scope a sandbox workspace per conversation):

- `list` — names and sizes of whatever's currently attached.
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

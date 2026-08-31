# Chat sessions: picking, continuing, closing, temporary

A chat "session" already existed before this feature — a random ID in
the browser's localStorage, keyed to a stored transcript server-side
(`internal/webui/server.go`'s `chatSession`) — but it was implicit: one
tab meant one session, and the only control was "Clear chat," which
discarded the old one and started a new one in a single click. This adds
an explicit picker: a list of past sessions to continue, a way to close
one without leaving it, and an ephemeral kind that never gets saved at
all.

## What's new vs. what already existed

Already there (unchanged): the session ID lives in `localStorage`
(`bosun-session-id`), sent as `session_id` on every `POST /api/chat`;
history is stored server-side in an in-memory map, optionally persisted
to `~/.local/share/bosun/sessions.json`; `GET /api/history?session_id=`
hydrates one session's transcript; `TTL`/`MaxSessions`
(`SessionOptions`) evict old sessions the same way regardless of how
they were created.

New:
- **`chatSession.Title`** — set once, from the first user message
  (`titleFromMessage`, first line only, truncated to 60 chars), never
  overwritten after that.
- **`GET /api/sessions`** — every non-ephemeral session, newest first:
  `{"sessions": [{"session_id", "title", "updated_at",
  "message_count"}, ...]}`.
- **`chatRequest.Temporary`** — only has an effect on the turn that
  actually creates a session (an unseen `session_id`); marks it
  `chatSession.Ephemeral`, fixed for that session's lifetime.
- The sessions dialog (gear-adjacent 💬 icon): continue a listed
  session, close one (`POST /api/session/clear`, already existed, now
  reachable for any session, not just the current one), or start a new
  session — regular or temporary.

## Why "temporary" means "never persisted," not "short TTL"

Ephemeral is a property of the session, checked in exactly two places
that already existed for an unrelated reason:
- `persistLocked` skips it when writing the disk snapshot.
- `handleSessionsList` skips it when building the picker list.

Everything else about an ephemeral session is completely ordinary — same
`TTL`, same `MaxSessions` eviction, same in-memory hydration via
`GET /api/history` for as long as the process keeps running. This was
the simpler of two options considered: a short/immediate TTL would still
have meant writing it to disk at least once (a leftover from a crash
before the shorter expiry fired), where "never gets a `Title`/never
included in a disk write" cleanly means a temporary chat leaves no trace
at all past a server restart or explicit close.

## Why a `<dialog>`, not a slide-out sidebar

Every other secondary panel in this UI (settings, file dump, cameras,
metric merges) is a `<dialog>` opened by a header icon — switching
sessions is the same shape of action as those: a discrete "leave this
context, go do something else," not something done *while* composing a
message. A persistent sidebar would need restructuring the page's base
layout (there's currently no left/right column at all) for a workflow
this app doesn't really have — a personal single-user assistant, not a
multi-conversation-at-once client.

## "Clear chat" became "New chat"

The old button's one-click behavior — discard the current session,
start a fresh one — no longer deletes anything; it's exactly
`startNewSession(false)`, the same function the sessions dialog's "+
Новый чат" button calls. The session being left is still in the picker
list unless explicitly closed there. The label changed from "Очистить
чат"/"Clear chat" to "Новый чат"/"New chat" to match: nothing is being
cleared anymore, a new one is being started.

## API

- `GET /api/sessions` → `{"sessions": [...]}`, newest-`updated_at`
  first, ephemeral sessions excluded. Sorting happens on the real
  `time.Time`, not the formatted string — two sessions updated within
  the same second would otherwise tie and fall back to Go's randomized
  map iteration order.
- `POST /api/chat` gained `temporary` (bool, default `false`) alongside
  the existing `message`/`language`/`session_id`. Sending it on a turn
  for a session that already exists does nothing — a session's
  `Ephemeral` flag is fixed at creation.
- `POST /api/session/clear` `{"session_id"}` — unchanged; already fully
  removed a session (in-memory and on disk) before this feature, now
  reachable from the picker for any listed session, not just the one
  the "Clear chat" button used to discard.
- `GET /api/history?session_id=` — unchanged; still how "Continue"
  rehydrates the picked session's transcript into the visible chat.

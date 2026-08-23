# Adventure: a voice-playable text adventure

An optional feature: a port of the original 1977 Colossal Cave Adventure,
playable through Старпом's own chat/voice interface — for when there's
nothing to do, no connection, or you're driving. Off by default; see
"Enabling it" below.

## Why this exists as a separate repo plus a thin integration here

The game engine itself lives in
[go-adventure](https://github.com/rromenskyi/go-adventure), a public,
zero-dependency Go module — a stripped fork of
[andrewsjg/goAdventure](https://github.com/andrewsjg/goAdventure), which
ported [Eric S. Raymond's open-adventure](https://gitlab.com/esr/open-adventure)
(the canonical portable C rewrite of the original game) to Go. It exposes
a plain call/response API (`advent.Game.ProcessCommand`/`.Output`) with
no I/O assumptions at all — not a CLI, not a service, just a library.

Everything in `internal/adventure/` here is the *integration*: SQLite
persistence for named sessions and an `adventure_game` tool the LLM can
call. The engine stays a pure library; nothing about how Старпом plays
it lives in go-adventure itself.

## Two ways to play

**The opportunistic path.** `adventure_game` is a normal LLM tool,
registered like `run_code` or `get_weather`. The model decides on its
own, during regular conversation, when to call it — "let's play", "go
north", "what am I holding" all work as regular chat messages. Its
result gets narrated by the model like any other tool's, in Старпом's
own voice; there's no special-casing. This means playing this way
always costs at least one LLM call per turn, same as it would for any
tool-using exchange.

An earlier version of this tool tried to fake a "zero LLM calls"
guarantee by having the *tool* short-circuit the agent's loop the
instant its result carried a "narration off" marker. Live testing
caught the real flaw: a single user message that asked for more than
one game action ("start a session, then go inside, then check
inventory") got silently truncated to just the first action, because
the short-circuit returned to the user before the model got a chance to
decide whether it wanted to call the tool again. Multi-step tool
chaining is a real, if secondary, capability the agent loop supports
for every tool — this exception broke it just for this one. Reverted:
the opportunistic tool now behaves exactly like every other tool.

**Game mode.** A per-conversation toggle (`POST /api/adventure/mode`)
that routes chat input straight into the game engine
(`internal/adventure.Store.Play`, called from `handleChat`/
`handleChatStreaming` in `internal/webui/server.go`), bypassing the
LLM/tool-calling loop entirely — this is where the "bored, no
connection, driving" case actually gets its **zero-LLM-call**
guarantee, since it's a single direct call, not a loop, and so can't
have the truncation failure mode above. `AdventureConfig.NarrateLocal`/
`NarrateRemote` decide, per currently active provider, whether a turn's
reply is the engine's raw text verbatim (default) or gets one extra
plain (tool-less) LLM call to rephrase it — never a multi-step loop.
Turning game mode on requires naming an existing session and is itself
a plain, LLM-free write (`internal/webui/adventure.go`'s
`handleAdventureMode`) — deliberately, so choosing what to play never
depends on a model being available to ask.

The response to a game-mode turn carries `location_id` (`chatResponse`
in `internal/webui/server.go`), but only on a turn that actually moved
the player — never on every turn — so the UI swaps the location's
art/ambient audio exactly when it should and no more often.

## Playing it

A 🎮 button appears in the header once the feature is enabled. Session
management lives in its own settings tab (list, create, delete, "Play")
— all plain REST calls, no LLM involved. Clicking "Play" turns game
mode on for the current browser tab's conversation; the 🎮 button then
toggles it back off. While on, ordinary chat messages go straight to
the engine; `#adventure-scene` above the message list shows the current
location's art/ambient audio once `tools/artgen`'s manifest exists (a
missing `/static/adventure/manifest.json` — the normal case until that
pipeline has actually been run and its output copied in — just means
no art/audio, not an error).
Verified live with a real browser (Playwright): settings tab, session
create/list, Play, the 🎮 toggle, and a full chat round-trip through
game mode, confirmed reaching the engine directly (~450ms end to end,
no LLM call) rather than the chat asker.

## Persistence (`internal/adventure/store.go`)

A dedicated SQLite file (`~/.local/share/bosun/adventure.db` by
default — same convention as `internal/metrics`), independent of any
other store in this project, holding:

- **sessions** — one row per named game, storing the engine's own
  serialized state (`advent.Game.SaveToBytes()`/`LoadFromBytes()` —
  reuses the engine's real save-file format, just persisted to a
  database column instead of a file) plus denormalized turn count,
  current location, and game-over flag for cheap listing.
- **history** — an append-only log of every command/output pair per
  session. Informational only — debugging and any future "show me what
  happened" UI — never read back to reconstruct state, which always
  comes from `sessions.state`.
- **memos** — an append-only, per-session scratchpad, meant for a
  future narration layer's own notes about a session (e.g. "player
  seems stuck near the grate"). **Structurally separate from, and never
  touching, Старпом's own memo/notes feature** — a deliberate
  constraint from the original design brainstorm for this feature.
- **active_sessions** — which named game session a given chat
  conversation is currently pointed at, so the LLM (or, later, the
  settings page) doesn't have to keep repeating a session name every
  turn. Set by the tool's `new_session`/`select_session` actions today;
  the plan is for the settings page to be able to set this directly too
  — session selection must not require an LLM call to work, for the
  same offline-first reason narration doesn't.

## Enabling it

```yaml
adventure:
  enabled: true          # off by default
  narrate_local: false   # game mode: rephrase via LLM when serving locally?
  narrate_remote: false  # game mode: rephrase via LLM when serving remotely?
```

Or via environment: `SMARTHELPER_ADVENTURE_ENABLED=true`.

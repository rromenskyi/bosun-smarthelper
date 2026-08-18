# Token Budget for the Local Model

The remote model has a huge context window and doesn't care how the prompt is
built. The local fallback model does — it's meant to run on weak, "nano"-class
hardware, often a 0.8B–2B model with a small context window (2k–4k tokens is
common). Every request pays a fixed tax before the user's actual question:
system prompt + tool contract + conversation history. This doc tracks what
that tax costs and what keeps it in check, so a future change doesn't
accidentally reintroduce a model-confusing bloat regression.

## What's on the request every turn

1. **System prompt** (`internal/agent/agent.go`, `systemPrompt` + persona
   line) — currently ~60 tokens.
2. **Tool contract** — every tool the connectivity state allows
   (`Registry.AvailableList`), sent as JSON Schema for native tool-calling
   APIs (Ollama, OpenAI). Measured with all 7 built-in tools registered:

   | State | Tools | Full JSON Schema |
   |-------|-------|-------------------|
   | online | 7 | ~3000 bytes (~750 tokens) |
   | offline | 4 | ~1500 bytes (~380 tokens) |

   The local model is usually only invoked while offline (router prefers
   remote when online), which already gets the smaller 4-tool set for free.
   The exception: general internet is up but the remote call itself fails
   (bad key, quota, outage) — the router falls back to local mid-request, and
   that local call still carries the full 7-tool online contract. This is the
   worst case worth designing for.
3. **Conversation history** — `web.history.local` / `web.history.remote`
   (`internal/config/config.go`, `internal/webui/server.go`). Two separate
   budgets, selected per request from current connectivity, not one shared
   number — see below.

## What ships today

- **Compact tool rendering for the weakest models**
  (`internal/llm/local.go`: `compactToolDefinitions`, used by
  `chatWithPromptedTools`). Models that can't do native tool calling at all
  (`llm.local.supports_tools: false`) are exactly the smallest models, so
  their tool contract skips the JSON Schema envelope
  (`type`/`properties`/`additionalProperties`) and renders one line per tool
  instead, e.g. `memo(action:write|read|list|archive|delete, key?:string): ...`.
  Measured saving: **~67–69%** smaller than the full JSON Schema form (online:
  ~750 → ~250 tokens; offline: ~380 → ~120 tokens).
- **Per-provider history budget, not one shared number.** A flat budget has
  to be sized for the weakest case, which either wastes the remote model's
  context or risks overflowing the local model's — there's no single number
  that's right for both. Instead (`internal/config/config.go`,
  `internal/webui/server.go`):
  - `web.history.local` (default: 4 turns / 4000 chars, ~1000 tokens) is what
    actually gets sent to the model on a request served while offline.
  - `web.history.remote` (default: 40 turns / 60000 chars, ~15000 tokens) is
    the storage cap — how much a session keeps in memory regardless of
    provider — and what gets sent on a request served while online.
  - `handleChat` in `internal/webui/server.go` decides which to *send* per
    request from `Status.Online`; `saveTurn` always *persists* up to the
    larger `Remote` budget. A turn served locally therefore only trims what
    that one request sees — it never deletes history from the session, so
    the very next turn, if served remotely, sees the full stored history
    again. Nothing is lost by falling back to local; the small window is a
    per-request view, not group amnesia. See
    `TestServerChatHistoryLocalVsRemoteBudget` in
    `internal/webui/server_test.go`.
  - Same caveat as the tool contract above: this switches on raw
    connectivity, not on which provider actually ends up answering, so the
    "online but remote fails mid-call" case still sends the large Remote
    slice to a local model that ends up serving it.
- **Offline tool trimming** (`docs/offline-mode.md`) already drops
  network-dependent tools (`web_search`, `wikipedia`, `open_meteo` weather)
  from the contract when there's no connectivity — fewer tools is also fewer
  tokens, not just a correctness fix.
- Tool definitions are listed in sorted, stable order
  (`Registry.List`/`AvailableList`) so the system-prompt-plus-tools prefix is
  byte-identical across turns when nothing changes, letting llama.cpp reuse
  its prompt/KV cache instead of reprocessing it from scratch.

## Not done yet (deliberately deferred, not forgotten)

- **Native tool-calling paths (Ollama, OpenAI-compatible) still send full
  JSON Schema.** Real function-calling APIs expect that shape, so this isn't
  simply portable from the compact-prompted path. A genuinely tiny model
  using native tool calling (rather than the prompted fallback) still pays
  the full ~750/380-token tax.
- **The tool set doesn't shrink just because local ends up serving a
  request while online.** `Agent` decides which tools to expose from raw
  connectivity, not from which provider actually answers — it can't know that
  ahead of the call, since `Router` only falls back to local *after* trying
  remote. `Router.CurrentProvider()` already exists and could inform a
  smaller, local-specific tool subset (e.g. a configured allowlist), but that
  changes tool availability semantics and needs a decision on the config
  shape, so it's a proposal, not a change made unilaterally.
- **Per-tool schema wording wasn't trimmed** in the canonical
  `InputSchema()`/`Description()` (used by all paths, including capable
  remote models) — the compact renderer already captures the big win for the
  weakest-model path specifically, and shortening descriptions everywhere
  would trade real clarity (e.g. the weather tool's landmark-disambiguation
  guidance) for a comparatively small additional saving.

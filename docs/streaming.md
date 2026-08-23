# Streaming

The web UI's answer fills in progressively instead of appearing all at once.
CLI (`smarthelper chat`) and the MCP server are unaffected — this is purely
a web-transport and rendering change.

## Wire protocol

`POST /api/chat` writes newline-delimited JSON instead of one object, when
the underlying asker supports it (`Content-Type: application/x-ndjson`):

```
{"type":"queued","position":1}
{"type":"step_start"}
{"type":"delta","kind":"prose","text":"На палубе"}
{"type":"delta","kind":"fold","text":"<tool_call>...</tool_call>"}
{"type":"step_start"}
{"type":"delta","kind":"prose","text":"Сейчас 21°C."}
{"type":"done","session_id":"..."}
```
or `{"type":"error","message":"..."}` in place of `done`. `queued` appears
at most once, only when the request had to wait — see "Only the local
model queues" below. One `step_start` per LLM call in the agent's
tool-calling loop; `kind: "fold"` marks text that shouldn't be shown as
plain prose (see below). Once the first line is flushed, the HTTP status
is locked at 200 — success/failure is the event type, not the status code,
since there's no way to change it mid-stream. A non-streaming asker (any
test double implementing only `Ask` or `AskWithHistory`) still gets the
old single-JSON response; the client checks `Content-Type` and handles
either — but never sees a `queued` event, since the buffered protocol has
no way to send anything before the final response (see below).

## Heartbeats: surviving a stall an intermediary can't see the reason for

A remote generation can legitimately go quiet for a while mid-answer —
observed directly on this deployment's own remote provider (an
AirLLM-style backend, judging by the domain and its literal model name
`"text"`, which streams model layers in from disk rather than keeping
everything resident, and so can stall unevenly between tokens). `bosun`'s
own `web.request_timeout` (600s) tolerates that fine — but an
intermediary in front of it (this deployment sits behind a Cloudflare
tunnel, see `docs/cloudflare.md`) enforces its *own*, shorter,
non-configurable idle-between-chunks timeout that has nothing to do with
total request duration. A stall past that threshold gets the connection
killed at the edge, with nothing logged anywhere in `bosun` itself —
exactly what happened to a real phone request that failed with a generic
error after "thinking" for a while.

`handleChatStreaming` (`internal/webui/server.go`) runs a ticker
alongside the actual generation call: if `heartbeatInterval` (15s) passes
with no real event written, it sends `{"type":"ping"}`. This needs no
frontend change — the client's NDJSON parser only recognizes specific
`type` values and silently ignores anything else — so it's purely
connection upkeep, invisible to the user, that resets any intermediary's
idle timer without affecting the actual answer.

## Only the local model queues

`handleChat` used to serialize *every* chat request through one slot
(`chatSlot`), local or remote — but the remote provider handles concurrent
requests fine on its own; only the local model is weak, shared hardware
that can't usefully run more than one generation at a time. Now a request
only touches `internal/webui/local_queue.go`'s `localQueue` when
`Status.Online` is false; an online request is never queued or slowed down
by another request at all.

A local request that has to wait is told immediately via `{"type":
"queued", "position": N}` (`N` = how many turns are ahead of it — 1 means
next up, right after whoever's currently being served) instead of being
left to wait silently the way the old single-slot semaphore did. If its
context expires before its turn arrives, `localQueue.abandon` removes it
from the queue — or, if the turn was granted in the exact same instant
(a real race with Go's `select`), passes that turn on to the next waiter
instead of leaving the slot stuck thinking someone's still holding it.

`webui.Server.TryIdleAfter` (background memo tag normalization — see
docs/memo-search.md) follows the same split: it only checks/claims the
local slot when offline; when online, it only waits out its quiet period,
since there's no shared-hardware contention with a background LLM call to
a remote provider.

## Why "fold" exists: the local XML tool-call quirk

The real constraint that shaped this design: this host's actual local setup
(`llama-server --skip-chat-parsing`) emits tool calls as raw
`<tool_call>...</tool_call>` XML mixed into ordinary content — sometimes
*after* real prose ("I will check.\n\<tool_call\>..."). There's no
structured "this is a tool call" signal until the closing tag arrives, so a
naive character-by-character stream would flash raw XML at the user.

`internal/llm/sse.go`'s `foldDetector` handles this: it holds back a small
tail of unflushed text (long enough that a marker split across two network
chunks is still caught) and reclassifies everything from the first
`<tool_call>` onward as `kind: "fold"`. The same detector also strips a
leaked reasoning-marker prefix (see `stripLeakedReasoningMarker` in
`docs/token-budget.md`'s neighborhood) before the very first flush, so that
leak never reaches the screen either. Both quirks are specific to
`--skip-chat-parsing`'s raw passthrough — the remote OpenAI-compatible API
and Ollama's native format both signal tool calls via a separate structured
field, so `RemoteClient` and Ollama-format `LocalClient` stream every
content delta as plain prose with no marker-scanning at all.

The web UI renders a `fold` delta into a collapsed `<details>` element
inside the same chat bubble — expandable if you want to see exactly what
was called and with what arguments, never erased. No "retract" mechanism
was needed once folding replaced the original erase-after-the-fact idea.

**Explicitly out of scope**: the prompted-JSON fallback
(`llm.local.supports_tools: false`, for models with no tool-call template
support at all) stays fully buffered — it needs the complete response to
recognize its `{"tool": "..."}` JSON blob, and that's already the
weakest-model-fallback-of-a-fallback path, not worth the added complexity.

## Escape hatch: `llm.local.stream: false`

Streaming is a byte-for-byte pass-through of whatever the model server
sends per chunk. That's fine when the server buffers correctly, but at
least one local setup on this host (`llama-server --skip-chat-parsing`
serving `mradermacher/gemma-4-E2B-it-GGUF`) corrupts multi-byte UTF-8 —
Cyrillic text arrives with literal `�` replacement characters —
specifically in streaming mode. The identical prompt sent with
`stream:false` to the same server produces correct output, so this is an
upstream llama.cpp/tokenizer limitation for that model, not something a
smarter client-side chunk boundary (`foldDetector`'s lookback window
already handles markers split across well-formed UTF-8 chunks) can recover
from — the bytes are already lossy by the time they reach the client.

Setting `llm.local.stream: false` (default `true`) makes
`LocalClient.ChatStream` skip SSE/NDJSON parsing entirely: it calls the
existing buffered `Chat()` and emits the whole answer as one `prose`
delta. This keeps the working per-token streaming code intact for
`RemoteClient` and any local server that doesn't exhibit the bug — it's a
per-deployment config toggle, not a codebase-wide behavior change.

Separately, Gemma's raw tool-call template (`<|tool_call>call:name{...}`)
doesn't match the Qwen-style `<tool_call><function=...>` XML this app's
parser and `foldDetector` marker are built for, so native tool calls
silently misfire. That's unrelated to streaming and is worked around with
`llm.local.supports_tools: false`, which routes tool use through the
model-agnostic prompted-JSON fallback (see the README's "Why the local
llama.cpp setup uses XML tool calls" section) instead of relying on any
specific chat-template syntax.

## Retry and fallback only apply pre-flight

`Router.ChatStream` keeps the same retry-then-fallback-to-local behavior as
the non-streaming path, with one rule: once a single delta has reached the
caller for a given attempt, that attempt no longer retries or falls back to
a different provider. A failure after that point surfaces as an error for
that step instead — there's no good way to silently swap voices mid-stream
or "un-show" a partial answer. See `TestRouterChatStream_RetriesBeforeFirstDelta`
and `TestRouterChatStream_NoRetryOrFallbackAfterDeltaSent` in
`internal/llm/router_test.go`.

## Verified live

Confirmed against the real deployed service: real per-token streaming from
the remote provider (each syllable of a Russian sentence arrived as its own
delta), a tool call's JSON result correctly appearing as a `fold` delta, and
`step_start` correctly separating the tool-decision step from the final
answer.

# Streaming

The web UI's answer fills in progressively instead of appearing all at once.
CLI (`smarthelper chat`) and the MCP server are unaffected — this is purely
a web-transport and rendering change.

## Wire protocol

`POST /api/chat` writes newline-delimited JSON instead of one object, when
the underlying asker supports it (`Content-Type: application/x-ndjson`):

```
{"type":"step_start"}
{"type":"delta","kind":"prose","text":"На палубе"}
{"type":"delta","kind":"fold","text":"<tool_call>...</tool_call>"}
{"type":"step_start"}
{"type":"delta","kind":"prose","text":"Сейчас 21°C."}
{"type":"done","session_id":"..."}
```
or `{"type":"error","message":"..."}` in place of `done`. One `step_start`
per LLM call in the agent's tool-calling loop; `kind: "fold"` marks text
that shouldn't be shown as plain prose (see below). Once the first line is
flushed, the HTTP status is locked at 200 — success/failure is the event
type, not the status code, since there's no way to change it mid-stream.
A non-streaming asker (any test double implementing only `Ask` or
`AskWithHistory`) still gets the old single-JSON response; the client
checks `Content-Type` and handles either.

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

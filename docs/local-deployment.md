# Local deployment (historical — superseded by Docker)

**Retired.** Both `llama-server.service` and `smarthelper-web.service` are
now stopped and disabled; everything (`bosun`, `llama-chat`, `llama-embed`)
runs via `docker compose` — see `docs/docker.md` for current operation. This
page is kept for the still-relevant reasoning below (`--skip-chat-parsing`,
CPU-backend tuning), which `deploy/llama/Dockerfile` now implements instead
of `~/server-llm-2b.sh`.

The original bare-metal setup: `llama-server` on `http://localhost:1234/v1`,
model alias `default`, one inference slot, 8192-token context to keep memory
use reasonable on this host. The Bosun local-client timeout is 120 seconds
because the first uncached tool-enabled prompt can take roughly one minute
on this CPU (an old Mac Mini — see `deploy/llama/Dockerfile` for why its
CPU-backend flags matter here specifically).

### Why `--skip-chat-parsing` is required

Originally installed for a Qwen GGUF (since replaced by Gemma — see
`docs/streaming.md` for what changed and why `llm.local.supports_tools:
false` is now set instead of relying on this adapter). Qwen's chat template
emitted tool calls in a template-specific XML form:

```xml
<tool_call>
<function=get_weather>
<parameter=location>Salt Lake City</parameter>
</function>
</tool_call>
```

llama.cpp's built-in chat parser did not reliably expose those calls as the
OpenAI-compatible `message.tool_calls` objects expected by the agent. Running
with `--skip-chat-parsing` preserves the raw model output instead of losing or
misclassifying the call. `internal/llm/local.go` then recognizes the
`tool_call`, `function`, and `parameter` tags and converts them into the
canonical internal call format. The existing agent executes the tool and sends
its structured result back to the model for the final natural-language answer.

The purpose is compatibility, not a second public protocol: remote models,
the MCP server, and the web client continue to use their normal JSON contracts.
The adapter only handles output from the local GGUF template. If the model or
server has no usable native/template tool support, configure
`llm.local.supports_tools: false`; Bosun will ask for a single strict
JSON tool-call object through the prompt instead.

That prompted-JSON fallback (`chatWithPromptedTools` in
`internal/llm/local.go`) tolerates two things a weak model routinely gets
wrong: narration wrapped around the JSON object ("Sure, I'll check: {...}
let me know"), and parameters flattened onto the top-level object instead of
nested under `"arguments"` (`{"tool":"web_search","query":"..."}` instead of
the documented `{"tool":"web_search","arguments":{"query":"..."}}`). Both
were observed on the deployed Gemma model and, before the fix, silently
turned a real tool argument into an empty one instead of erroring loudly.

Stable alphabetical tool ordering is intentional. It keeps the large prefix
of repeated local prompts byte-for-byte consistent, improving the chance that
llama.cpp can reuse its prompt/KV cache and reducing latency on this CPU.

## Web interface

Browser URL: `http://10.0.0.111:8080` (unchanged from the bare-metal setup —
`web.bind` in `config.yaml`). See `docs/docker.md` for the current
`docker compose` operations. The UI has no authentication and must remain on
the trusted private LAN; the bind validator rejects wildcard and public
addresses.

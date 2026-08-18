# Local deployment

This machine runs the local Qwen model through a systemd-managed llama.cpp
server.

## Service

- Unit: `llama-server.service`
- Unit file: `/etc/systemd/system/llama-server.service`
- Launch script: `/home/roman220/server-llm-2b.sh`
- API: `http://localhost:1234/v1`
- Model alias: `default`

Common operations:

```bash
sudo systemctl status llama-server.service
sudo systemctl restart llama-server.service
sudo journalctl -u llama-server.service -f
```

The service is enabled at boot and uses `Restart=on-failure`. The launch
script reads `LOCAL_LLM_API_KEY` from the repository's gitignored `.env` file;
the key must never be written into the script, unit, or committed config.

The server binds to `127.0.0.1`. The LAN web interface calls it through the
Bosun backend; the raw LLM API is not exposed to the network.

The current launch script uses one inference slot and an 8192-token context to
keep memory use reasonable on this host. The Bosun local-client timeout
is 120 seconds because the first uncached tool-enabled prompt can take roughly
one minute on this CPU.

### Why `--skip-chat-parsing` is required

The installed Qwen GGUF includes a chat template that emits tool calls in a
template-specific XML form:

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

Stable alphabetical tool ordering is intentional. It keeps the large prefix
of repeated local prompts byte-for-byte consistent, improving the chance that
llama.cpp can reuse its prompt/KV cache and reducing latency on this CPU.

## Web interface

- Unit: `smarthelper-web.service`
- Unit file: `/etc/systemd/system/smarthelper-web.service`
- Browser URL: `http://10.0.0.111:8080`
- Configured bind: `10.0.0.111:8080`

Common operations:

```bash
sudo systemctl status smarthelper-web.service
sudo systemctl restart smarthelper-web.service
sudo journalctl -u smarthelper-web.service -f
```

The web unit starts after `llama-server.service` and automatically restarts on
failure. The UI has no authentication and must remain on the trusted private
LAN. The bind validator rejects wildcard and public addresses.

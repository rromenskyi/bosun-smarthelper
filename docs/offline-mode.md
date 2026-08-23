# Offline Mode

Bosun selects both the LLM provider and the tools exposed to that model
from the current connectivity state.

## Runtime behavior

1. The router checks `llm.router.check_target` using the configured timeout.
2. When online, the preferred remote model and all configured tools are
   available.
3. When offline, requests use the local model and network-dependent tools are
   omitted from its function/tool schema.
4. The agent adds an offline instruction telling the model not to claim or
   offer live web data.
5. If connectivity disappears after a tool schema was created, the agent
   rejects a network-dependent call before executing it.
6. `/api/status` returns `available_tools`; the browser hides unavailable quick
   actions using the same list.

The current `open_meteo` weather backend, DuckDuckGo search, and Wikipedia
require internet access. The weather `mock` backend, memo storage, fridge mock
backend, GPS mock backend, and native system metrics remain available offline.

The LLM connectivity check is intentionally conservative: if the configured
check target or remote service is unavailable, Bosun enters offline
mode even if an unrelated public endpoint might still respond.

`Router.CheckConnectivity` retries a check target that fails
(`connectivityCheckAttempts`, `connectivityCheckRetryDelay` in
`internal/llm/router.go`) before actually flipping to offline — a real
report showed a single slow/dropped `HEAD` request against a remote
provider that's occasionally slow (see `docs/streaming.md`'s heartbeat
section) routing the very next request or two to the local model even
though nothing was actually wrong with the connection, just that one
round-trip. A real, sustained outage still fails every attempt within the
same short window, so genuine offline detection isn't meaningfully
delayed — only a single bad round-trip is now forgiven. A failure that
survives all retries is logged (`Router.SetLogger`) instead of flipping
silently, which it did before.

## Adding an online tool

Implement the normal `tools.Tool` interface and also implement:

```go
type NetworkDependentTool interface {
    RequiresNetwork() bool
}
```

Return `true` only when the tool's selected backend requires a public network.
A tool with both local and online backends should decide from its configuration,
as `WeatherTool` does for `mock` versus `open_meteo`.

Do not rely only on prompt wording. Availability filtering and the execution
guard are the enforcement mechanisms; the prompt merely helps the model
explain offline limitations naturally.

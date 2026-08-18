# Memo and document semantic search

`memo`'s `search` action finds memos — and, once a document store is wired
in, uploaded reference documents (manuals, how-tos) — by meaning, not just
exact words. "What did I say about the fridge?" matches a memo about a
faulty compressor even if it never uses the word "fridge."

## Why two separate stores

- **Memos** (`internal/tools/memo.go`): short personal notes, one embedding
  per whole note. Written, read, listed, archived, and deleted through the
  `memo` tool — the model can create these itself.
- **Documents** (`internal/documents`): long reference text. Too long to
  embed as one vector without losing precision, so it's split into
  paragraph/sentence-bounded chunks (`chunkText` in
  `internal/documents/chunker.go`) and each chunk gets its own embedding.
  Uploaded **only through the web UI** (`POST /api/documents`, plain-text
  files — convert a PDF to text first) — deliberately **not** an
  LLM-callable tool action. A weak local model already struggles with a
  handful of tool definitions (see `docs/token-budget.md`); adding
  upload/list/delete as tool actions would grow the contract for a
  capability only a human ever needs (nobody asks the assistant out loud to
  ingest a file). Search is the only document capability exposed to the
  model, and it reuses the existing `memo` tool's `search` action rather
  than registering a second tool — so the contract doesn't grow at all.

## Chunking

Paragraphs (blank-line-separated) already under ~1500 characters are kept
whole, so real paragraph boundaries survive untouched. Only an oversized
paragraph is split further, at sentence boundaries — greedily packing whole
sentences until the next one would exceed the limit — so a chunk never cuts
a sentence in half. This means chunks are uneven in size by design; that
trade-off was chosen deliberately over fixed-size windows, which are
simpler but can slice a sentence in two.

## Ranking and graceful degradation

Both stores use the same `internal/embeddings.Client` (an OpenAI-compatible
`/embeddings` call) and `embeddings.CosineSimilarity`. `search` merges
memo and document hits into one relevance-sorted list, tagged
`"source": "memo"` or `"source": "document"`.

Nothing about this feature can turn a working memo/document write into a
failure:

- `llm.embeddings.base_url` empty (the default) — no embeddings are
  computed at all; `search` falls back to a plain substring match instead
  of erroring.
- Embeddings configured but the server is unreachable when writing a memo
  or uploading a document — the write still succeeds, just without a
  vector; that item won't be found by semantic search until re-saved, but
  substring search still finds it.
- Embeddings configured but unreachable at *search* time — same substring
  fallback, per store, independently.

## Why a second local llama-server instance

Neither this deployment's remote proxy (`405 Method Not Allowed` on
`/v1/embeddings`) nor the local chat model (`llama-server` needs
`--embeddings` at startup, and a chat-tuned model isn't ideal for
embeddings anyway) can serve embeddings. `llama-embed` in
`docker-compose.yml` runs a small multilingual embedding model
(`nomic-ai/nomic-embed-text-v2-moe-GGUF`, chosen for solid Russian support
since that's this deployment's primary language) dedicated to this — see
`docs/docker.md`.

## Config

```yaml
llm:
  embeddings:
    base_url: "http://localhost:1235/v1"  # empty disables the feature
    model: "embed"
    timeout: 10s

documents:
  path: ""  # empty uses ~/.local/share/bosun/documents.json
```

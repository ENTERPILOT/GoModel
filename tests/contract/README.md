# Contract Replay Tests

Contract tests in this project are **adapter replay tests**.

They do not call external APIs in CI. Instead, they replay recorded provider payloads from `testdata/` through the real provider adapters and verify the normalized `core` output.

## What is validated

- Real adapter parsing paths (`ChatCompletion`, `StreamChatCompletion`, `ListModels`, `Responses`, `StreamResponses`)
- Streaming conversion behavior (`[DONE]`, chunk/event mapping)
- Provider-specific conversion logic (for example Anthropic -> OpenAI-compatible / Responses output)

## How replay works

1. A custom in-memory `http.RoundTripper` returns recorded fixtures for expected method/path routes.
2. Provider adapters are constructed with `NewWithHTTPClient(...)` and pointed at a local replay base URL.
3. Tests call adapter methods directly and assert normalized outputs.

No sockets are opened and no network access is required.

## Fixture layout

```text
testdata/
├── openai/
├── anthropic/
├── gemini/
├── groq/
├── kimicode/
└── xai/
```

Each folder contains recorded JSON and SSE payloads used by replay tests.

## Running

The CI workflow runs this suite in the `test-contract` job (`.github/workflows/test.yml`).

```bash
# Run contract replay tests
go test -v -tags=contract -timeout=5m ./tests/contract/...

# Make target
make test-contract
```

## Updating fixtures

Contract tests under `tests/contract/**/*_test.go` must validate full normalized output against committed golden files.
When `finish_reason == "tool_calls"`, golden output must include `message.tool_calls[]` entries
with `id`, `type`, and `function{name,arguments}`.

Use the canonical recorder target to refresh provider payload fixtures:

```bash
make record-api
```

Then refresh normalized contract-output goldens from replay tests:

```bash
RECORD=1 go test -v -tags=contract -timeout=5m ./tests/contract/...
```

Re-run the suite without `RECORD=1` before committing.

## `recordapi` Endpoints

When recording fixtures manually with `cmd/recordapi`, available endpoint options are:

- `chat`: `POST /v1/chat/completions`
- `chat_stream`: `POST /v1/chat/completions` with `"stream": true`
- `models`: `GET /v1/models`
- `responses`: `POST /v1/responses`
- `responses_stream`: `POST /v1/responses` with `"stream": true`

Example request body for `responses`:

```json
{
  "model": "gpt-4o-mini",
  "input": "Say 'Hello, World!' and nothing else."
}
```

Example request body for `responses_stream`:

```json
{
  "model": "gpt-4o-mini",
  "input": "Say 'Hello, World!' and nothing else.",
  "stream": true
}
```

Notes:

- The `-model` flag in `cmd/recordapi` overrides `"model"` for these request bodies.
- `responses` capability is currently supported by `openai` and `xai` in recording mode.
- Running `responses`/`responses_stream` for unsupported providers returns a local capability error before any network call.

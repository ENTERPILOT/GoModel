# STATUS: feat/kimicode-native-responses

Branch: `feat/kimicode-native-responses` (base upstream/main d6f8924a)

## Done
- `internal/providers/kimicode/kimicode.go`: native Responses support.
  `Provider` embeds `*openai.ChatCompatible` + `responses *openai.CompatibleProvider`
  (shared `compatibleConfig`, Bearer auth). `SetBaseURL` overrides both.
  `Responses`/`StreamResponses` reject `previous_response_id` first
  (`core.NewInvalidRequestError`), then forward via `adaptResponsesRequest`
  (pins `store=true`→false, copy-on-write, nil passthrough).
- `internal/providers/kimicode/kimicode_test.go`: contract test
  (`NativeResponses: true`, `Embeddings: true`), `TestAdaptResponsesRequest`,
  rejection tests (zero upstream calls), native endpoint tests
  (JSONServer + SSEServer golden), `TestSetBaseURL`.
- `docs/providers/kimicode.mdx`: native /v1/responses forwarding,
  store rewrite, previous_response_id rejection.

## Verification
- gofmt: clean
- go vet ./internal/providers/kimicode/: clean
- go build ./...: ok
- go test ./internal/providers/kimicode/ ./internal/testconventions/ -count=1: ok
- golangci-lint run internal/providers/kimicode/...: 0 issues
- coverage: every kimicode.go function 100%
- contract suite (`-short -tags=contract` over internal/providers/...,
  internal/core, internal/testconventions, tests/contract/...): all ok

## Remaining
- Nothing. Work complete and committed.

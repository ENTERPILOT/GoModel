# GoModel Testing Strategy

A 3-layer testing strategy with **DB state verification** as the highest priority:

```text
┌─────────────────────────────────────────────────────────────┐
│  Layer 3: Contract Replay Tests (Provider Compatibility)     │
│  - Golden files with real API responses                      │
│  - Replay through real provider adapters in CI               │
├─────────────────────────────────────────────────────────────┤
│  Layer 2: Integration Tests (DB Verification) ← PRIORITY    │
│  - Real PostgreSQL/MongoDB via Docker-managed containers    │
│  - Verify DB state after each request                       │
│  - Field completeness assertions                            │
├─────────────────────────────────────────────────────────────┤
│  Layer 1: Unit + E2E Tests (Current - unchanged)            │
└─────────────────────────────────────────────────────────────┘
```

## Test Architecture Overview

```
tests/
├── e2e/                    # End-to-end tests (in-process mock server)
│   ├── *_test.go           # E2E test files (requires -tags=e2e)
│   └── mock_provider.go    # Mock provider for testing
├── contract/               # Contract tests (golden file validation)
│   ├── *_test.go           # Contract test files (requires -tags=contract)
│   ├── main_test.go        # Shared helpers and types
│   ├── README.md           # Documentation and curl examples
│   └── testdata/           # Golden files from real API responses
│       ├── openai/
│       ├── anthropic/
│       ├── gemini/
│       ├── xai/
│       └── groq/
└── integration/            # Integration tests (DB verification)
    └── *_test.go           # DB state verification tests

internal/
├── providers/
│   └── *_test.go           # Unit tests alongside implementation
├── cache/
│   └── *_test.go
└── ...
```

## Layer 1: Unit + E2E Tests

### Unit Tests

Located alongside implementation files (`*_test.go`). Test individual components in isolation.

```bash
# Run unit tests only
make test

# Run specific package tests
go test ./internal/providers -v -run TestName
```

### E2E Tests

In-process mock server tests. No Docker required.

```bash
# Run E2E tests
make test-e2e

# Run specific E2E test
go test -v -tags=e2e ./tests/e2e/... -run TestName
```

**Key characteristics:**

- Tests full request flow through the gateway
- Uses mock providers (no real API calls)
- Validates routing, transformation, and response handling
- Fast execution, suitable for CI

## Layer 2: Integration Tests (DB Verification)

Real database testing with Docker-managed containers. **Priority for data integrity.**

```bash
# Run integration tests (requires Docker)
go test -v -tags=integration ./tests/integration/...
```

**Key characteristics:**

- Real PostgreSQL/MongoDB via Docker-managed containers
- Verify DB state after each request
- Field completeness assertions
- Validates audit logging, usage tracking

**Focus areas:**

- Audit log entries are complete and accurate
- Usage metrics are properly recorded
- Request/response pairs are correctly stored
- Token counts match expected values

## Layer 3: Contract Replay Tests (Provider API Compatibility)

Golden file replay tests validating provider adapter behavior. No API calls in CI.

```bash
# Run contract tests
go test -v -tags=contract -timeout=5m ./tests/contract/...

# Run specific provider tests
go test -v -tags=contract -timeout=5m ./tests/contract/... -run TestOpenAI
```

**Key characteristics:**

- Golden files contain real API responses (recorded manually)
- Tests replay payloads through real adapters (`ChatCompletion`, streaming, models, `Responses`)
- No network calls during test execution
- Detects API contract changes and adapter parsing regressions

### Supported Providers

| Provider  | Endpoint                          | Features Tested                                                  |
| --------- | --------------------------------- | ---------------------------------------------------------------- |
| OpenAI    | api.openai.com                    | Chat, streaming, models, tools, JSON mode, multimodal, reasoning |
| Anthropic | api.anthropic.com                 | Messages, streaming, tools, extended thinking, multimodal        |
| Gemini    | generativelanguage.googleapis.com | Chat, streaming, models, tools (OpenAI-compatible)               |
| xAI       | api.x.ai                          | Chat, streaming, models (OpenAI-compatible)                      |
| Groq      | api.groq.com                      | Chat, streaming, models, tools (OpenAI-compatible)               |

### Golden File Structure

```
tests/contract/testdata/
├── openai/
│   ├── chat_completion.json           # Basic chat
│   ├── chat_completion_reasoning.json # o3-mini reasoning
│   ├── chat_completion_stream.txt     # SSE streaming
│   ├── chat_with_tools.json           # Function calling
│   ├── chat_json_mode.json            # Structured output
│   ├── chat_with_params.json          # Temperature, stop sequences
│   ├── chat_multi_turn.json           # Conversation history
│   ├── chat_multimodal.json           # Image input
│   └── models.json                    # Models list
├── anthropic/
│   ├── messages.json                  # Basic messages
│   ├── messages_stream.txt            # SSE streaming
│   ├── messages_with_params.json      # System, temperature
│   ├── messages_with_tools.json       # Tool use
│   ├── messages_extended_thinking.json # Reasoning
│   ├── messages_multi_turn.json       # Conversation
│   └── messages_multimodal.json       # Image input
├── gemini/                            # 5 files
├── xai/                               # 4 files
└── groq/                              # 5 files
```

### Recording New Golden Files

Use the standardized make target:

```bash
make record-api
```

Then validate replay tests with golden-file checks enabled:

```bash
go test -v -tags=contract -timeout=5m ./tests/contract/...
```

## Running All Tests

```bash
# All tests (unit + E2E)
make test-all

# Contract tests separately
go test -v -tags=contract -timeout=5m ./tests/contract/...

# Integration tests (requires Docker)
go test -v -tags=integration ./tests/integration/...

# Full CI pipeline
make lint && make test-all
```

## Test Commands Reference

| Command                                                   | Description                   |
| --------------------------------------------------------- | ----------------------------- |
| `make test`                                               | Unit tests only               |
| `make test-e2e`                                           | E2E tests with mock providers |
| `make test-all`                                           | Unit + E2E tests              |
| `go test -tags=contract -timeout=5m ./tests/contract/...` | Contract tests                |
| `go test -tags=integration ./tests/integration/...`       | Integration tests             |
| `make lint`                                               | Run golangci-lint             |

## CI/CD Integration

```yaml
# GitHub Actions example
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.26"

      # Unit + E2E (no external dependencies)
      - run: make test-all

      # Contract tests (no API calls, uses golden files)
      - run: go test -tags=contract -timeout=5m ./tests/contract/...

      # Integration tests (requires Docker)
      - run: go test -tags=integration ./tests/integration/...
```

## Best Practices

1. **Unit tests**: Test business logic in isolation, mock external dependencies
2. **E2E tests**: Test full request flow with mock providers
3. **Contract tests**: Validate API compatibility, update golden files when APIs change
4. **Integration tests**: Verify DB state, use real databases via Docker-managed containers

### When to Update Golden Files

- Provider releases new API version
- Response structure changes
- New fields are added to responses
- Testing new provider features

### Contract Test Maintenance

1. Run API calls manually or via `recordapi` tool
2. Save responses to `tests/contract/testdata/{provider}/`
3. Verify tests pass with new golden files
4. Commit golden files to version control

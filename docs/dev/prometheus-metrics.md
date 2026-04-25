# Prometheus Metrics — Implementation Notes

> **Status: experimental.** Metric names, labels, and the hooks API surface may
> change without notice. This document targets contributors. For user-facing
> configuration, see [`docs/guides/prometheus-metrics.mdx`](../guides/prometheus-metrics.mdx).

## Overview

Metrics collection is decoupled from provider business logic through a small
hook system in `internal/llmclient`. Hooks are wired in once at startup; the
provider implementations themselves are not aware of metrics.

## Wiring

```text
cmd/gomodel/main.go
  └─ if cfg.Metrics.Enabled:
       factory.SetHooks(observability.NewPrometheusHooks())

internal/providers/factory.go
  └─ ProviderFactory.SetHooks(hooks llmclient.Hooks)
       stores hooks on the factory; each provider built via the factory
       receives them through llmclient.Config.Hooks.

internal/llmclient/client.go
  └─ Client invokes hooks at the logical-request level:
       beginRequest  → Hooks.OnRequestStart
       finishRequest → Hooks.OnRequestEnd

internal/observability/metrics.go
  └─ NewPrometheusHooks() returns the Prometheus implementation:
       OnRequestStart: increments in-flight gauge
       OnRequestEnd:   decrements gauge, records duration, increments counter
```

When `METRICS_ENABLED=false` (the default), `factory.SetHooks` is never
called, the factory's `hooks` field stays at its zero value, and no metrics
are recorded. The `/metrics` endpoint also returns 404 in that case.

## Hook API

Defined in `internal/llmclient/client.go`:

```go
type RequestInfo struct {
    Provider string
    Model    string
    Endpoint string
    Method   string
    Stream   bool
}

type ResponseInfo struct {
    Provider   string
    Model      string
    Endpoint   string
    StatusCode int           // 0 if network error
    Duration   time.Duration
    Stream     bool
    Error      error         // nil on success
}

type Hooks struct {
    OnRequestStart func(ctx context.Context, info RequestInfo) context.Context
    OnRequestEnd   func(ctx context.Context, info ResponseInfo)
}
```

`OnRequestStart` returns a `context.Context` so hooks can attach trace spans
or request IDs that flow through the rest of the request.

## Instrumentation Points

Hooks fire at the **logical request** level via `beginRequest` /
`finishRequest`, not per HTTP attempt. This means a request that retries 3
times produces one counter increment, not three.

Three call sites in `client.go` use them:

1. `DoRaw` — non-streaming requests (chat completions, responses, models)
2. `DoStream` — streaming requests; `OnRequestEnd` fires when the stream is
   established, **not** when it closes (known limitation)
3. `DoPassthrough` — provider-native passthrough requests

## Metrics Exposed

All metrics are served at the configured endpoint (default `/metrics`).

### `gomodel_requests_total`

Counter. Total LLM requests.

Labels: `provider`, `model`, `endpoint`, `status_code`, `status_type`,
`stream`.

`status_type` is `"success"` or `"error"`. `status_code` is the HTTP status
code as a string, or `"network_error"` when the upstream call failed before
returning a response.

### `gomodel_request_duration_seconds`

Histogram. Request latency.

Labels: `provider`, `model`, `endpoint`, `stream`.

Buckets: `0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60` seconds.

For streaming requests, this measures time-to-stream-establishment, not the
total stream duration.

### `gomodel_requests_in_flight`

Gauge. Concurrent in-flight requests.

Labels: `provider`, `endpoint`, `stream`.

### `gomodel_response_snapshot_store_failures_total`

Counter. Failures while persisting response snapshots (used by the response
cache / audit pipeline, not the LLM request path itself).

Labels: `provider`, `provider_name`, `operation`.

## Helpers in `client.go`

- `extractModel(body any) string` — pulls `Model` from `*core.ChatRequest` or
  `*core.ResponsesRequest`. Returns `"unknown"` for other request types.
- `extractStatusCode(err error) int` — pulls `StatusCode` from
  `*core.GatewayError`; returns `0` for network errors.

## Testing

Unit tests live in `internal/observability/metrics_test.go`:

```bash
go test ./internal/observability/...
```

They cover hook registration, success/error labelling, network errors,
streaming, in-flight counting, and duration recording.

For end-to-end verification:

```bash
export METRICS_ENABLED=true
./bin/gomodel
# in another shell:
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $GOMODEL_MASTER_KEY" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"Hi"}]}'
curl -s http://localhost:8080/metrics | grep gomodel_requests_total
```

## Extending to Other Backends

The hooks API is provider-agnostic. To add another backend, return a
different `llmclient.Hooks` value from a constructor in
`internal/observability` and wire it in `cmd/gomodel/main.go`:

```go
// example sketch — combineHooks does not exist today
factory.SetHooks(observability.NewPrometheusHooks())
```

If multiple backends are needed simultaneously, a small `combineHooks` helper
that calls each set in turn would be the natural addition. It is intentionally
not implemented yet — add it when there is a second backend, not before.

## Possible Follow-Ups

Not committed; listed so contributors don't redesign the same things:

- Token usage labels on duration / counter (requires plumbing usage from the
  response into `ResponseInfo`).
- Cache hit/miss counters for the response and model caches.
- Circuit-breaker state gauge per provider.
- Request/response payload size histograms.

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
run/providers.go (defaultProviderFactory)
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
    Provider     string
    ProviderType string
    Model        string
    Operation    string // semantic GenAI operation; "" for non-inference calls
    Endpoint     string
    Method       string
    Stream       bool
}

type ResponseInfo struct {
    Provider     string
    ProviderType string
    Model        string
    Operation    string
    Endpoint     string
    StatusCode   int           // 0 if network error
    Duration     time.Duration
    Stream       bool
    Error        error         // nil on success
    CircuitState string        // "closed", "half-open", "open"; "" when the breaker is disabled
}

type EmptyResponseInfo struct {
    Provider     string
    ProviderType string
    Model        string
    Operation    string
    Reason       string // "no_choices", "no_output", "no_usage"
}

type Hooks struct {
    OnRequestStart     func(ctx context.Context, info RequestInfo) context.Context
    OnRequestEnd       func(ctx context.Context, info ResponseInfo)
    OnStreamFirstChunk func(ctx context.Context, info ResponseInfo) // first bytes of a successful stream
    OnStreamEmpty      func(ctx context.Context, info ResponseInfo) // stream ended before its first byte
    OnEmptyResponse    func(ctx context.Context, info EmptyResponseInfo) // fired by the provider router
}
```

`ResponseInfo` and `RequestInfo` omit the stream-intent fields
(`StreamUncertain`) here for brevity; see the source for the full structs.

`OnRequestStart` returns a `context.Context` so hooks can attach trace spans
or request IDs that flow through the rest of the request.

## Instrumentation Points

The llmclient hooks fire at the **logical request** level via `beginRequest` /
`finishRequest`, not per HTTP attempt. This means a request that retries 3
times produces one counter increment, not three. `OnEmptyResponse` is the
exception: the provider router fires it, not the llmclient (see
`gomodel_empty_responses_total`).

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

### `gomodel_circuit_breaker_state`

Gauge. Circuit breaker state per provider: `0` closed, `1` half-open, `2`
open. Updated on every request completion (including requests the open
breaker rejects), so an idle provider keeps its last observed state; the
series is absent for providers whose breaker is disabled
(`failure_threshold: 0`).

Labels: `provider`.

Alerting example: `gomodel_circuit_breaker_state == 2` for more than a
minute means a provider is being actively short-circuited.

### `gomodel_empty_responses_total`

Counter. Buffered chat and Responses API calls that returned 200 without
choices (`no_choices`), without output (`no_output`, completed Responses API
calls only), or without token usage (`no_usage`). The provider router detects
these after decoding and fires `Hooks.OnEmptyResponse`; the llmclient never
calls it. It fires once per provider call: llmclient retries within one call
count once, but a failover target is another call, so a request whose primary
and failover both return empty increments the counter twice.
`gomodel_requests_total` still records these calls as successes.

Labels: `provider`, `model`, `reason`.

## Token and Cost Metrics

These do not come from the llmclient hooks: a hook sees the provider call, not
the decoded usage. They come from the usage pipeline instead.

```text
internal/app/usage_metrics.go (wrapUsageLoggerForMetrics)
  └─ if cfg.Metrics.Enabled or OpenTelemetry is on:
       usage logger = usagemetrics.NewTap(logger, callers, recorders...)

internal/usagemetrics
  └─ Tap.Write(entry)        every finalized usage entry, streaming or not
       newSample(entry)      tokens and cost, read once
       Callers.Resolve(...)  opt-in caller dimensions, bounded
       Recorder.RecordUsage  one call per enabled pipeline

internal/observability/usage.go   UsageRecorder → gomodel_* counters
internal/telemetry/usage.go       usageRecorder → gen_ai.* / gomodel.* instruments
```

The tap is the innermost usage-logger decorator: the billable and markup taps
wrap it, so an entry arrives with its billed cost set. It does no I/O and is
not installed when both pipelines are off. Entries exist only while usage
tracking is on (`USAGE_ENABLED`), so the metrics need it too.

### `gomodel_tokens_total`

Counter. Tokens processed by providers.

Labels: `provider`, `model`, `token_type` (`input`, `output`), `user_path`,
`auth_key`, `request_labels`.

`input` is the whole prompt (uncached + provider-cached + cache-write, from
`usage.EntryInputSegments`, the split the dashboard uses) and `output`
includes reasoning (`usage.EntryReasoningTokens` adds it for providers that
report it on top of completion tokens).

### `gomodel_token_details_total`

Counter. Parts of `gomodel_tokens_total`; a separate metric so that no sum
over one metric counts a token twice.

Labels: as above, with `token_type` one of `input_cached`,
`input_cache_write`, `output_reasoning`.

### `gomodel_provider_cost_usd_total`, `gomodel_billed_cost_usd_total`

Counters, in USD. Provider cost is the entry's `TotalCost`; billed cost is
its `BilledCost` (set by the markup tap, so absent while markups are off).
Two metrics for the same reason as the token details.

Labels: `provider`, `model`, `user_path`, `auth_key`, `request_labels`.

### `gomodel_caller_requests_total`

Counter. Provider calls by caller, recorded by the `OnRequestEnd` hook only
while a caller dimension is enabled.

Labels: `provider`, `model`, `status_type`, `user_path`, `auth_key`,
`request_labels`.

### What an entry adds

| Entry | Tokens | Provider cost | Billed cost |
| --- | --- | --- | --- |
| Provider call | yes | when priced | when markups are on |
| Response-cache hit | no | no | when cache hits are billed |
| MCP tool call | none recorded | none recorded | none recorded |

This matches the invoice export: tokens and provider cost cover requests sent
to providers, billed cost also covers billed cache hits. Zero values are
skipped, so a series appears with its first non-zero value.

### Caller dimensions and cardinality

`user_path`, `auth_key`, and `request_labels` are opt-in
(`metrics.caller_dimensions`) and shared by both pipelines. A dimension that
is off is an empty label in Prometheus and a missing attribute in
OpenTelemetry.

- The user path is cut to `user_path_depth` segments.
- `auth_key` is the key's name, looked up from the in-memory key snapshot; an
  unknown key ID is `{unknown}`. The ID and the secret are never exported.
- `request_labels` is the sorted, comma-joined label set. One series per set
  keeps sums exact; one series per label would count a multi-label request
  once per label.
- Each dimension admits `max_values` distinct values and reports the rest as
  `{other}` (`/{other}` for user paths). The admitted sets are process-wide
  (`usagemetrics.admitted`), because Prometheus series outlive a reload.

Caller labels are deliberately kept off `gomodel_requests_total`,
`gomodel_request_duration_seconds` (one series per bucket), and
`gomodel_requests_in_flight`. `gomodel_caller_requests_total` carries them
instead, without `endpoint`, `status_code`, and `stream`. None of the new
metrics has an `endpoint` label: usage entries of passthrough calls carry
caller-chosen paths.

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

Token and cost metrics are tested in `internal/usagemetrics` (samples, caller
dimensions, the value cap), `internal/observability/usage_test.go`, and
`internal/telemetry/usage_test.go`.

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
`internal/observability` and wire it in `run/providers.go`:

```go
// example sketch — combineHooks does not exist today
factory.SetHooks(observability.NewPrometheusHooks())
```

If multiple backends are needed simultaneously, a small `combineHooks` helper
that calls each set in turn would be the natural addition. It is intentionally
not implemented yet — add it when there is a second backend, not before.

## Possible Follow-Ups

Not committed; listed so contributors don't redesign the same things:

- Cache hit/miss counters for the response and model caches.
- Request/response payload size histograms.

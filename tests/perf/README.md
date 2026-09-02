# Performance Checks

Run the deterministic hot-path guard with:

```bash
make perf-check
```

The CI job and pre-commit hook both run this guard. The current allocation and
byte ceilings live in `tests/perf/hotpath_test.go`. The same command also
measures median routing overhead for TTS, STT, realtime WebSocket setup, and
WebRTC SDP signaling against a zero-delay local mock provider. Those scenarios
fail above the 5 ms threshold in `tests/perf/voice_routing_latency_test.go`; no
provider account, API key, or billable request is used. Direct and routed calls
alternate order, and the guard uses the median of their paired latency deltas.

Run the underlying benchmarks with allocation output:

```bash
make perf-bench
```

## Bare vs. routed hot path

`BenchmarkGatewayHotPathChatCompletion` passes a bare provider to `server.New`
and isolates serialization + middleware cost. It does **not** exercise model
resolution.

`BenchmarkGatewayHotPathChatCompletionRouted` wires a real `Router` +
`ModelRegistry` (the production shape) with a representative catalog, so it
covers the per-request resolution path. Resolution goes through an O(1)
selector index, so the routed path costs only a few allocations more than the
bare one and is independent of catalog size.

`BenchmarkGatewayHotPathChatCompletionRoutedAlias` and
`BenchmarkGatewayHotPathChatCompletionRoutedQualified` send the request shapes
production clients actually use: an alias (`fast`) resolved through a model
resolver, and a provider-qualified selector (`mock/gpt-4o-mini`). Both are
guarded alongside the bare routed case, so a regression that only shows up
when the model has to be rewritten cannot hide behind the bare benchmark.

`BenchmarkSharedStreamingObserversDefaultConfig` covers streaming observation
with audit body capture disabled (the default), where the observed stream
skips JSON decoding for chunks no observer wants.

## Production shape and ablation

`BenchmarkGatewayHotPathProductionShape` runs the routed path with the
default-deployment middleware chain fully wired: master-key auth, audit
logging (bodies + headers), usage tracking, session keeping, and a configured
rate limit. The guard enforces allocation ceilings on this case too, so
regressions in any of those subsystems fail CI even though the bare cases
cannot see them.

The `BenchmarkAblation*` family turns exactly one subsystem off relative to
that full shape; the delta against `BenchmarkAblationFull` attributes
per-request cost to that subsystem. These are diagnostic benchmarks (run via
`make perf-bench`), not guarded.

`TestSessionIDVisibilityByBodySize` pins that content-based session
auto-detection is independent of request body size — there is no size above
which a request quietly stops carrying a session id to downstream consumers.

# Performance Checks

Run the deterministic hot-path guard with:

```bash
make perf-check
```

The CI job and pre-commit hook both run this guard. The current allocation and
byte ceilings live in `tests/perf/hotpath_test.go`.

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

`BenchmarkSharedStreamingObserversDefaultConfig` covers streaming observation
with audit body capture disabled (the default), where the observed stream
skips JSON decoding for chunks no observer wants.

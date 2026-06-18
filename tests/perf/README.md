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
covers the per-request resolution path. This routed path currently allocates an
order of magnitude more per request because resolution re-copies the full model
catalog several times; its guard ceilings should tighten significantly once
resolution is computed once per request and reused.

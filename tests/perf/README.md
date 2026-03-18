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

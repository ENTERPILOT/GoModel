"""qalib — small helpers for the GoModel quality (QA) harness.

Stdlib-only. Split into focused modules so each stays readable:
  config      — gateway URL, master key, model/image role resolution, spec loading
  paths       — JSON-path mini-language + deterministic image fixtures
  assertions  — declarative assertion operators
  client      — HTTP send (JSON / multipart / SSE) + audit-log lookup
  report      — console table + results.json + report.md
"""

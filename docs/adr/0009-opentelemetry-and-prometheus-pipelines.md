# ADR-0009: OpenTelemetry and Prometheus stay separate pipelines

## Context

GoModel has two observability outputs:

- **Prometheus** (`METRICS_ENABLED`): the `gomodel_*` gateway metrics, defined
  with `prometheus/client_golang` in `internal/observability` and pulled from
  `METRICS_ENDPOINT`. GoModel Pro adds `gomodel_pro_*` metrics the same way.
- **OpenTelemetry** (`OTEL_ENABLED`, since #802): HTTP server spans and metrics
  plus GenAI provider-call spans and metrics, defined with the OpenTelemetry
  SDK in `internal/telemetry` and pushed over OTLP.

Both attach to the same points — the `llmclient` provider hooks and the Echo
middleware stack — but instrument independently. Neither sees the other's
metrics: a Prometheus scrape does not include `gen_ai.*`, and an OTLP backend
does not receive `gomodel_*`.

The OpenTelemetry SDK can serve a Prometheus scrape endpoint through
`go.opentelemetry.io/otel/exporters/prometheus`, so a single set of instruments
could feed both outputs. That would remove the duplicated instrumentation and
give each backend the full metric set. It would not remove a dependency: the
OTel Prometheus exporter is itself built on `client_golang`.

## Decision

Keep the two pipelines independent for now. Metrics are defined twice, with
each library's native API, and each output carries only its own set.

The reason is the maturity of the OTel Prometheus exporter, not the design.
The exporter is still on a `v0.x` release line and its scrape-output rules
(unit suffixes, `_total` handling, `otel_scope_*` labels, namespace handling)
have changed across releases. Building GoModel's scrape surface on it would
mean tracking those changes while GoModel's own Prometheus surface is still
marked experimental. Two boring pipelines are cheaper than one that moves
under us.

Rules that follow from this:

- New gateway metrics are added to Prometheus with `client_golang`, as today.
- OpenTelemetry emits semantic-convention signals only; it is not a second
  transport for `gomodel_*` metrics.
- The OpenTelemetry HTTP middleware excludes the configured Prometheus endpoint
  (together with health and pprof), so scrapes never produce spans or inflate
  HTTP metrics.
- Enabling both is supported and costs the sum of both hook sets.

## Consequences

- Operators who want both `gomodel_*` and `gen_ai.*` in one backend route the
  Prometheus scrape and the OTLP stream separately (for example a collector
  with a Prometheus receiver alongside the OTLP receiver).
- The semconv metrics are not available to a Prometheus-only deployment, and
  the gateway metrics are not available to an OTLP-only one.
- Instrumentation for a new provider-call signal has to be added in two places
  if it belongs in both outputs.

## Revisit when

The OTel Prometheus exporter reaches a stable release, or GoModel's Prometheus
metrics leave experimental status. At that point the intended direction is
"one instrumentation API, two export paths": define every metric once with
the OpenTelemetry API and attach a Prometheus reader under `METRICS_ENABLED`
and an OTLP reader under `OTEL_ENABLED`, preserving the existing `gomodel_*`
names so dashboards keep working. Pro's metrics would move through the same
path. Expect roughly one extra microsecond per request from the OTel metric
SDK compared with `client_golang`, and re-baseline the performance guard.

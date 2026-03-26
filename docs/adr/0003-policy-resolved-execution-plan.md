# ADR-0003: Policy-Resolved Execution Plan

## Context

GOModel already has a request-scoped `ExecutionPlan` runtime object, but today it
is derived in middleware and lives only in request context.

That is not enough for the next stage of the gateway.

GOModel needs:

- durable control over request preprocessing behavior
- one plan selected per request, with deterministic matching
- immutable plan history so a request can be traced back to the exact plan used
- in-memory lookup for the hot path
- a storage-backed source of truth that works in future clustered deployments

ADR-0002 establishes the ingress boundary:

- immutable raw request capture via `RequestSnapshot`
- optional best-effort semantic extraction via `WhiteBoxPrompt`

This ADR defines the next layer above that boundary: how execution plans are
stored, matched, loaded into memory, and referenced from requests.

## Decision

### 1. Keep Two Distinct Concepts

GOModel keeps two related but distinct concepts:

1. persisted execution plan versions
2. request-scoped `core.ExecutionPlan`

Persisted execution plan versions are the control-plane source of truth.

`core.ExecutionPlan` remains the request-scoped runtime projection consumed by
handlers, middleware, and provider execution code.

The runtime object is derived from:

- the matched persisted execution plan version
- request facts captured from ingress
- request-scoped resolution results such as endpoint metadata and model
  resolution

### 2. First-Slice Scope Model

The first slice supports exactly these scopes:

- global
- provider
- provider plus model

Examples:

- `(provider=NULL, model=NULL)` means the single global execution plan
- `(provider=openai, model=NULL)` means the provider-scoped plan
- `(provider=openai, model=gpt-5)` means the provider-plus-model plan

This ADR does not yet define path-scoped, key-scoped, team-scoped, or
organization-scoped execution plans.

### 3. Matching Rule

Exactly one execution plan version is selected for a request.

Matching uses most-specific-wins precedence:

1. `provider + model`
2. `provider`
3. `global`

There is no runtime layering or composition in this slice.

If a more specific plan exists, it fully replaces the less specific plan for
that request.

### 4. Persistence Model

The first slice uses a single append-only execution-plan table.

Each row represents one immutable execution plan version.

Suggested fields:

- `id`
- `scope_provider` nullable
- `scope_model` nullable
- `version`
- `active`
- `name`
- `plan_payload`
- `plan_hash`
- `created_at`
- optional operator metadata such as `description`

Rules:

- rows are immutable after creation
- changing a plan means inserting a new row
- for a given scope, only one row may be active at a time
- requests reference the immutable row id of the matched version
- `scope_provider=NULL, scope_model!=NULL` is invalid in this slice

This means the database row id is the execution plan version identity.

### 5. Hot-Path Runtime Model

The database is the source of truth, but request matching must not depend on
database reads.

GOModel loads active execution plan rows into memory and serves request matching
from an immutable in-memory snapshot.

The in-memory snapshot should expose:

- one global plan pointer
- one map keyed by provider
- one nested map keyed by provider and model

Request-time lookup should be:

1. determine request selector inputs
2. check `provider + model`
3. else check `provider`
4. else use `global`
5. materialize request-scoped `core.ExecutionPlan`
6. attach the matched immutable execution plan version id to request context

Snapshot refresh must be atomic so hot-path reads never observe a partially
reloaded plan set.

This allows each GOModel instance to keep a fast local read model while sharing
one persistent source of truth across a cluster.

### 6. Request Traceability

Each request must be traceable to the exact immutable execution plan version
that was selected.

The first required persistence surface is `audit_logs`.

`audit_logs` should store:

- `execution_plan_version_id`

This id is sufficient for request explainability because the referenced
execution-plan row is immutable.

The first slice does not require storing the same field in `usage`.

Usage records may continue to link back to audit records through `request_id`.

### 7. V1 Plan Payload

The first slice keeps execution plans intentionally simple.

The gateway keeps the overall request-processing order predefined.

Execution plans do not define a general workflow graph in v1.

Instead, a matched execution plan configures:

- simple feature flags for gateway-owned behaviors
- guardrail execution order inside the predefined guardrails phase

Human-facing metadata such as the plan name belongs in the immutable database
row for the execution-plan version, not in the JSON payload.

Recommended v1 payload shape:

```json
{
  "schema_version": 1,
  "features": {
    "cache": true,
    "audit": true,
    "usage": true,
    "guardrails": true
  },
  "guardrails": [
    {
      "ref": "pii-redaction",
      "step": 10
    },
    {
      "ref": "prompt-injection-check",
      "step": 10
    },
    {
      "ref": "system-prompt-inject",
      "step": 20
    }
  ]
}
```

V1 semantics:

- the top-level gateway flow stays hardcoded
- the `guardrails` array configures only the guardrails phase
- guardrails are sorted by numeric `step`
- guardrails with the same `step` run in parallel
- later steps start only after the previous step fully completes
- if `features.guardrails` is `false`, the guardrails array is ignored
- `ref` must point to an existing named guardrail managed by the gateway

To preserve immutability, omitted feature flags may be accepted at authoring
time, but they must be resolved to explicit booleans before an immutable plan
version is stored.

In other words:

- missing feature flags may mean "use defaults" in write-time input
- persisted plan versions must store effective resolved values, not implicit
  defaults

This prevents the same immutable execution plan version from changing behavior
later if process-wide defaults drift.

### 8. Future Evolution

The payload schema may grow later to support richer preprocessing or execution
flows.

That future expansion should build from the v1 shape instead of starting with a
general-purpose workflow DSL before there is a concrete need for one.

## Consequences

### Positive

- **Cluster-ready source of truth**: All instances can load execution plans from
  the same database
- **Deterministic matching**: One request maps to one plan version using a
  simple precedence rule
- **Fast hot path**: Request matching uses in-memory snapshots instead of
  per-request database reads
- **Immutable traceability**: Audit records can point to the exact plan version
  used
- **Clear control-plane boundary**: Persisted plan versions become the durable
  policy layer, while `core.ExecutionPlan` remains request-scoped runtime state
- **Simple v1 payload**: The first implementation stays focused on flags plus
  ordered guardrails instead of a premature workflow engine

### Negative

- **More runtime machinery**: The gateway now needs plan loading, validation,
  indexing, and refresh behavior
- **Activation constraints**: The database must enforce one active plan per
  scope
- **Limited expressiveness in v1**: Only the guardrails phase is explicitly
  configurable beyond feature toggles
- **Broader test surface**: Matching precedence, immutability, refresh, and
  audit linkage all need focused tests

## Notes

This ADR intentionally stays focused on runtime and storage architecture.

It does not define:

- plan authoring APIs
- admin UI behavior
- rollout workflows
- a general-purpose workflow graph
- path or key hierarchy

Those can be added later on top of this storage and matching model.

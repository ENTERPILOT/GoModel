# ADR-0002 Rollout Plan

## Goal

Adopt the `IngressFrame` and `SemanticEnvelope` architecture from [ADR-0002](../adr/0002-ingress-frame-and-semantic-envelope.md) while fixing current OpenAI-compatible field loss.

The immediate trigger is that unknown JSON fields such as `response_format` can be dropped before an OpenAI-compatible upstream provider receives the request.

## Current branch slice

The first implementation slice should preserve unknown top-level JSON fields for existing OpenAI-compatible endpoints without adding one-off typed fields.

That gives the gateway a safer baseline while the wider ADR rollout is implemented.

Completed in this slice:

- preserve unknown top-level JSON fields on `ChatRequest`
- preserve unknown top-level JSON fields on `ResponsesRequest`
- preserve unknown top-level JSON fields on `EmbeddingRequest`
- preserve those fields through handler binding and OpenAI provider request marshaling
- preserve them even through OpenAI o-series request rewriting
- add regression tests for JSON round-tripping, handler binding, and upstream provider forwarding
- preserve unknown nested JSON fields on normal non-batch chat messages, tool calls, content parts, and responses input elements
- preserve unknown nested JSON fields through normal handler decoding and OpenAI provider marshaling

## Rollout phases

### Phase 0: Lock the invariants

- define the no-loss invariants for model-facing requests before moving more code
- add regression coverage for chat, responses, embeddings, streaming, guardrails on/off, and provider rewrite paths
- treat unknown top-level and nested JSON fields as preservation targets unless the gateway intentionally rewrites them

Exit criteria:

- the branch has a documented preservation matrix for known failure paths
- tests fail when a request loses opaque fields during bind, guardrail rewrite, router adaptation, or provider adaptation

### Phase 1: Stop current field loss

- keep the current typed request structs for known behavior
- add opaque field preservation for request objects that are already exposed on `/v1/*`
- cover reported regressions such as `response_format: {type: "json_schema", ...}`
- make sure routing, audit logging, usage tracking, and guardrails continue to work unchanged
- explicitly cover nested request reconstruction points, not only top-level request fields

Exit criteria:

- unknown top-level OpenAI-compatible JSON fields survive bind -> router -> provider
- unknown nested fields are not lost in current guardrail and adapter rewrite paths for supported `/v1/*` endpoints
- unit tests cover chat, responses, embeddings, and rewrite-path preservation

### Phase 2: Introduce `IngressFrame`

- add an immutable request capture object at the transport boundary
- include method, path, params, query, headers, content type, raw body bytes, request ID, and trace metadata
- create it once in middleware after request ID assignment and before audit logging, auth, validation, and handlers
- store it in request context so downstream layers can read it without re-reading or mutating the body
- make audit logging read from the ingress frame when available instead of reconstructing from partial state
- add a thin `/p/{provider}/{endpoint}` opaque path early enough to validate transport-first handling before richer semantic work is complete

Exit criteria:

- model-facing handlers consume a shared ingress representation
- raw request bytes are available to later semantic extraction and pass-through flows
- selector extraction no longer requires ad hoc body reads from separate middleware

### Phase 3: Introduce `SemanticEnvelope`

- add best-effort semantic extraction from `IngressFrame`
- represent dialect, operation kind, selector hints, canonical request fields, and opaque extras separately from raw ingress
- allow partial semantics instead of forcing every request into a full typed schema
- split minimal selector extraction from richer canonical extraction so routing does not depend on a fully populated semantic envelope
- let routing use selector hints derived from ingress even when the semantic envelope is sparse

Exit criteria:

- handlers no longer own request semantics directly
- semantic extraction can succeed partially while preserving raw ingress losslessly
- route resolution works from ingress-derived selector hints without requiring full canonicalization

### Phase 4: Migrate existing features onto the new split

- adapt guardrails to operate on `SemanticEnvelope` canonical content
- adapt provider adapters to use canonical semantics when rewriting is required
- when only partial semantics are available, patch raw-plus-canonical payloads instead of rebuilding fresh structs
- fall back to raw ingress or preserved opaque extras when semantics are partial
- move `/p/{provider}/{endpoint}` onto the same ingress and semantic pipeline as `/v1/*`

Exit criteria:

- `/v1/*` and `/p/*` share one ingress pipeline
- pass-through and translated flows both preserve unknown fields unless the gateway intentionally rewrites them

## Design rules for implementation

- do not add one-off typed fields just to chase upstream API churn
- keep the ingress frame immutable
- never mutate raw ingress bytes to reflect semantic rewrites
- do not make routing depend on a fully populated semantic envelope
- prefer raw-plus-canonical patching over fresh struct reconstruction when only part of the request is understood
- keep guardrails and policy layers operating on semantic data, not transport data
- preserve secret redaction behavior in audit logs
- keep routing explicit and predictable

## Key risks

- guardrail rewrites can still drop unknown nested fields if the gateway reconstructs nested objects instead of preserving raw ingress
- provider-specific adapters may reintroduce field loss when they build fresh structs instead of rewriting raw-plus-canonical payloads
- batch and file flows will need the same transport-first treatment to avoid a second request pipeline

## Next implementation targets

1. Add a thin `/p/{provider}/{endpoint}` opaque passthrough route on the shared ingress pipeline.
2. Move more model-facing handlers, especially `/v1/batches` and later file flows where appropriate, onto ingress-first decoding instead of ad hoc request parsing.
3. Extend `SemanticEnvelope` from selector hints toward canonical operation content for `/v1/chat/completions`, `/v1/responses`, and `/v1/embeddings`.
4. Migrate non-batch guardrail and provider rewrite paths toward semantic canonical data plus raw-plus-canonical patching where partial understanding is unavoidable.

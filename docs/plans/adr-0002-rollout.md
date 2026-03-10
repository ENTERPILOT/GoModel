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
- keep the base `SemanticEnvelope` sparse at ingress time and cache canonical `ChatRequest`, `ResponsesRequest`, and `EmbeddingRequest` payloads lazily for the JSON routes the gateway already understands
- make chat, responses, and embeddings handlers consume those cached semantic request payloads instead of re-decoding the body independently
- preserve unknown top-level and batch-item JSON fields on `BatchRequest`
- make `/v1/batches` consume ingress-backed semantic decoding instead of `Bind()`
- add sparse batch route semantics for `/v1/batches*` so get/list/cancel/results handlers consume ingress-derived batch IDs and pagination metadata instead of ad hoc path/query parsing
- move inline batch item endpoint normalization and selector extraction into shared core semantic helpers so batch creation no longer hardcodes chat/responses/embeddings subrequest parsing in the HTTP handler
- make guarded chat rewrites preserve original message envelopes and structured content shapes in both normal and batch flows, including opaque message and content-part JSON fields
- harden `/p/{provider}/{endpoint}` so passthrough still works when guardrails wrap the router
- make passthrough use the same retry and circuit-breaker policy as the translated provider clients while still proxying raw upstream responses
- make `/v1/files*` ingress-managed with bounded multipart handling so file routes also receive `IngressFrame` without eagerly buffering upload bodies
- add sparse file semantics for `/v1/files*` so handlers consume shared provider, purpose, filename, file ID, and pagination metadata without trying to canonicalize uploaded bytes
- collapse the repeated JSON semantic decode/cache pattern behind one shared helper so the semantic layer gets slimmer as more endpoints move onto it

## Broader endpoint migration scope

ADR-0002 is not only about the current `/v1/chat/completions`, `/v1/responses`, and `/v1/embeddings` JSON flows.

The broader migration should cover four endpoint classes:

- translated OpenAI-compatible JSON endpoints under `/v1/*`
- opaque provider pass-through endpoints under `/p/{provider}/{endpoint}`
- batch endpoints under `/v1/batches*`
- file endpoints under `/v1/files*`

Pass-through scope should be explicit:

- first-class pass-through support should be added for OpenAI and Anthropic
- other providers should only be added in this ADR-0002 rollout if they can ride the same transport-first opaque forwarding path without significant provider-specific branching or new semantic translation work
- if a provider requires heavy request rewriting, custom streaming adaptation, or a special-case transport path, defer that provider to a later slice instead of complicating the ADR-0002 foundation

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
- make OpenAI and Anthropic the required first pass-through providers for this phase
- only add other providers in this phase when the same opaque forwarding path works with little or no provider-specific branching

Exit criteria:

- model-facing handlers consume a shared ingress representation
- raw request bytes are available to later semantic extraction and pass-through flows
- selector extraction no longer requires ad hoc body reads from separate middleware
- `/p/openai/{endpoint}` and `/p/anthropic/{endpoint}` run on the shared ingress capture path
- `/v1/files*` also receive `IngressFrame`, while multipart bodies remain intentionally uncaptured unless a later slice has a bounded reason to inspect them

### Phase 3: Introduce `SemanticEnvelope`

- add best-effort semantic extraction from `IngressFrame`
- represent dialect, operation kind, selector hints, canonical request fields, and opaque extras separately from raw ingress
- allow partial semantics instead of forcing every request into a full typed schema
- split minimal selector extraction from richer canonical extraction so routing does not depend on a fully populated semantic envelope
- keep the base semantic envelope cheap at ingress time; populate canonical request content lazily for operations the gateway understands well instead of eagerly cloning request JSON
- let routing use selector hints derived from ingress even when the semantic envelope is sparse
- keep pass-through semantic extraction intentionally sparse for provider-native routes unless the gateway already has a clear canonical understanding of the operation

Exit criteria:

- handlers no longer own request semantics directly
- chat, responses, and embeddings handlers consume canonical semantic request payloads rather than independently unmarshaling request bodies
- semantic extraction can succeed partially while preserving raw ingress losslessly
- route resolution works from ingress-derived selector hints without requiring full canonicalization
- provider pass-through routes do not require fake canonical schemas to execute

### Phase 4: Migrate existing features onto the new split

- adapt guardrails to operate on `SemanticEnvelope` canonical content
- adapt provider adapters to use canonical semantics when rewriting is required
- when only partial semantics are available, patch raw-plus-canonical payloads instead of rebuilding fresh structs
- fall back to raw ingress or preserved opaque extras when semantics are partial
- move `/p/{provider}/{endpoint}` onto the same ingress and semantic pipeline as `/v1/*`
- keep OpenAI and Anthropic as the reference pass-through implementations and only generalize further once that path stays simple

Exit criteria:

- `/v1/*` and `/p/*` share one ingress pipeline
- pass-through and translated flows both preserve unknown fields unless the gateway intentionally rewrites them
- the OpenAI and Anthropic pass-through routes do not depend on typed request structs for opaque forwarding

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
- file uploads still need careful discipline so sparse semantic metadata does not grow into a fake rich schema that pretends to understand multipart bytes
- trying to add too many provider-specific pass-through variants at once can turn the transport-first `/p/*` route into another adapter matrix before the shared foundation is stable

## Next implementation targets

1. Centralize model-facing endpoint classification so ingress capture, audit classification, and semantic extraction use one shared route descriptor table.
2. Add a thin `/p/{provider}/{endpoint}` opaque passthrough route on the shared ingress pipeline, with OpenAI and Anthropic as the required first providers.
3. Extend the same `/p/*` route to other providers only when they fit the same low-friction opaque forwarding model without meaningful extra branching.
4. Keep collapsing duplicate semantic decode boilerplate so `SemanticEnvelope` stays authoritative without growing one-off route helpers forever.
5. Migrate non-batch guardrail and provider rewrite paths toward semantic canonical data plus raw-plus-canonical patching where partial understanding is unavoidable.
6. Decide whether any additional provider-native routes under `/p/*` merit richer semantics or should remain intentionally sparse.

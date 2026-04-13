# ADR-0002: Ingress Frame and Semantic Envelope

## Context

GOModel already exposes OpenAI-compatible endpoints under `/v1/*` and is expected to add a provider pass-through API under `/p/{provider}/{endpoint}`.

The gateway also needs to support richer request shapes over time:

- JSON requests with known fields
- JSON requests with unknown or newer upstream fields
- file-based media inputs
- pass-through provider-native requests
- future audio and video workflows, including requests where semantic extraction is only partial

The current typed-request approach is too narrow for that future:

- it assumes JSON request bodies
- it silently drops unknown fields during struct decoding
- it treats semantic understanding as mandatory
- it does not naturally model pass-through requests

GOModel needs a model that preserves the original request faithfully while still allowing the gateway to extract and work with the parts it understands.

## Flow Diagram

![RequestSnapshot and WhiteBoxPrompt request flow](assets/0002-ingress-frame-flow.svg)

## Decision

Use `RequestSnapshot` and `WhiteBoxPrompt` for transport-bearing model and provider request routes such as `/v1/chat/completions`, `/v1/responses`, `/v1/embeddings`, `/v1/batches*`, `/v1/files*`, and `/p/{provider}/{endpoint}`.

Discovery routes such as `GET /v1/models` are out of scope.

`RequestSnapshot` is always present.

`WhiteBoxPrompt` is optional and best-effort. It may be rich, sparse, or absent, depending on how much the gateway understands about the route, content type, and request body.

This gives GOModel one consistent ingress model across both `/v1/*` and `/p/*`.

## RequestSnapshot

`RequestSnapshot` is the immutable capture of the inbound request at the transport boundary.

It should contain:

- method
- path
- route parameters
- query parameters
- headers
- content type
- raw body bytes
- request ID and tracing metadata

`RequestSnapshot` is transport-oriented, not schema-oriented.

Its job is to preserve what came over the wire so the gateway can:

- audit it reliably
- re-use it for pass-through when appropriate
- derive semantics from it without losing fidelity

`RequestSnapshot` must not be mutated.

## WhiteBoxPrompt

`WhiteBoxPrompt` is the gateway's best-effort semantic extraction from the `RequestSnapshot`.

It may contain:

- dialect, such as `openai_compat` or `provider_passthrough`
- operation kind, such as `chat_completions`, `responses`, `embeddings`, or provider-native operations
- selector hints, such as requested model, provider, and endpoint
- canonical request content for operations the gateway understands
- opaque request data preserved when extraction is partial

Examples of canonical fields include:

- chat: `messages`, `tools`, `stream`, `reasoning`
- responses: `input`, `instructions`, `tools`, `stream`

For JSON endpoints, the gateway may use a raw-plus-canonical extraction pattern inside the semantic layer:

- preserve the original JSON
- extract the subset the gateway understands
- keep unknown JSON fields instead of discarding them

Opaque preservation is behavioral, not structural: the gateway may satisfy it with raw ingress JSON and/or route-specific canonical request types. A generic envelope-level extras bag is optional.

Not every endpoint needs a rich semantic envelope.

Examples:

- `/v1/chat/completions` can usually have a rich semantic envelope
- `/v1/responses` can usually have a rich semantic envelope
- `/p/openai/responses` may have partial or rich semantics
- `/p/{provider}/{unknown-endpoint}` may have only selector and route metadata

The gateway must allow the semantic envelope to be partial or absent rather than forcing every request into a fake canonical schema.

## Streaming

For HTTP SSE endpoints, streaming remains a standard ingress request plus a streamed egress response.

`RequestSnapshot` captures the initial HTTP request only. The semantic envelope may include `stream=true` and any streaming options the gateway understands.

The streamed response is not part of `RequestSnapshot`. If the gateway needs structured handling of outbound SSE, it should use a separate egress stream abstraction that preserves raw event frames and optionally derives per-event semantics.

## Why This Is Good

- one consistent pipeline for `/v1/*` and `/p/*`
- audit logging always has a reliable raw source
- policy and guardrails can work when semantics are available
- passthrough still works when semantics are partial or absent
- future media and realtime support does not force a redesign

## Important Constraints

- do not require every endpoint to have a rich semantic envelope
- do not mutate the ingress frame
- do not force opaque requests into fake canonical schemas

## Consequences

### Positive

- **Uniform ingress model**: All model-facing endpoints use the same transport-first boundary
- **Better auditability**: The gateway always retains an authoritative raw request source
- **Graceful degradation**: Requests still flow even when only partial semantic understanding is available
- **Safer passthrough**: Opaque provider-native routes do not need to be forced through translated abstractions
- **Forward compatibility**: Unknown JSON fields can be preserved instead of being silently dropped
- **Foundation for future policy work**: Later workflow resolution and capability decisions can build on a stable ingress and semantics model

### Negative

- **Two representations to manage**: The raw request and semantic interpretation must remain clearly separated
- **More discipline required**: Code must avoid smearing semantic mutations back into the raw ingress frame
- **Partial semantics are unavoidable**: Some pass-through or media requests will remain only partially understood by the gateway

## Notes

If future realtime transports require it, the `RequestSnapshot` concept can be generalized later without changing the core decision here: preserve the transport input first, and derive semantics second.

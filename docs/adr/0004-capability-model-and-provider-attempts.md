# ADR-0004: Capability Model and Provider Attempts

## Context

Not every route or provider interaction can safely support the same gateway features.

Examples:

- `/v1/chat/completions` can usually support aliases, guardrails, and fallback
- `/v1/responses` can usually support similar features
- `/p/openai/responses` may support partial semantic extraction and selected policies
- `/p/{provider}/{unknown-endpoint}` may support only authentication, auditing, and raw pass-through

The gateway also needs a clean execution model for:

- retries
- fallback
- translated provider calls
- raw pass-through calls
- future async media jobs and result artifacts

Without explicit capability and attempt modeling, those behaviors become hard to validate, audit, and evolve.

## Decision

Introduce two related concepts:

1. `CapabilityModel`
2. `ProviderAttempt`

### CapabilityModel

Each route, dialect, and operation must advertise capabilities.

Examples include:

- semantic extraction supported
- guardrails supported
- fallback supported
- alias resolution supported
- request patching supported
- usage extraction supported
- streaming supported

`ExecutionPlan` may only enable behaviors that are valid for the request's capability set.

### ProviderAttempt

`ProviderAttempt` is the explicit outbound execution unit produced from the plan.

A single request may produce one or more attempts:

- one translated call to the primary provider
- a retried call
- a fallback call to an alternate provider
- a raw pass-through call

For async or media-oriented providers, an attempt may also produce:

- an operation handle
- lifecycle status such as queued, running, completed, or failed
- artifact references such as file IDs, media URLs, or result handles

## Consequences

### Positive

- **Safer feature gating**: The gateway will not try to apply unsupported features to opaque or provider-native routes
- **Cleaner fallback model**: Alternate calls become explicit attempts rather than hidden control flow
- **Better observability**: Audit logs and debugging can show which upstream attempts were made
- **Media and async ready**: Video, audio, and task-based provider APIs fit naturally into an attempt-oriented execution model
- **Clearer provider boundaries**: Capability declarations make route behavior predictable

### Negative

- **Capability registry required**: The project must maintain explicit knowledge of which operations support which features
- **More runtime state**: Multi-attempt execution and async operations add bookkeeping
- **Broader test surface**: Capability gating and attempt lifecycle behavior both need targeted tests

## Notes

This ADR builds on ADR-0002 and ADR-0003:

- ADR-0002 defines how requests enter the gateway
- ADR-0003 defines how policies are resolved into an execution plan
- this ADR defines how the plan is constrained and executed against providers

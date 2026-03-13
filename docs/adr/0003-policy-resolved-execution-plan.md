# ADR-0003: Policy-Resolved Execution Plan

## Context

GOModel is expected to support richer request processing policies over time, including decisions per model, API key, user, team, or organization, such as:

- which guardrails apply
- whether guardrails run in parallel or sequentially
- whether aliases are resolved
- whether failover is enabled
- whether translation or pass-through mode is used
- what resilience settings apply

Those concerns should not be scattered across handlers, provider adapters, and middleware.

ADR-0002 establishes the ingress boundary:

- immutable raw request capture via `RequestSnapshot`
- optional best-effort semantic extraction via `WhiteBoxPrompt`

GOModel still needs a single place where request processing policy is resolved into a concrete runtime decision.

## Decision

Introduce `ExecutionPlan` as the policy-resolved plan for handling a request.

`ExecutionPlan` is derived after authentication and identity resolution, using:

- `RequestSnapshot`
- `WhiteBoxPrompt`
- route and endpoint metadata
- resolved identity
- API key, user, team, and organization context
- configured policies and flow rules

`ExecutionPlan` should decide:

- whether the request is allowed
- requested versus resolved selector values
- alias resolution
- whether translation or passthrough mode is used
- which guardrails apply
- whether guardrails run in parallel or sequentially
- whether retries are enabled
- whether failover is enabled and what the failover chain is
- timeouts and resilience settings
- audit and usage policy

Handlers and provider adapters should consume the plan, not assemble policy decisions ad hoc.

## Consequences

### Positive

- **Clear control plane**: Request processing decisions live in one place
- **Future-ready policy model**: Per-key, per-team, and per-model behavior has an explicit architectural home
- **Less handler drift**: Endpoint handlers stay focused on ingress and response handling
- **Cleaner provider adapters**: Providers execute resolved behavior instead of owning policy logic
- **Better explainability**: Audit and debugging can show what plan was selected and why

### Negative

- **Additional abstraction**: The system now needs an explicit planning stage
- **More configuration pressure**: Policy inputs must be modeled cleanly to avoid an unstructured rule set
- **More tests required**: The project will need focused tests for policy resolution and execution selection

## Notes

This ADR is intentionally separate from ADR-0002. The ingress and semantic model should stay stable even as policy resolution evolves.

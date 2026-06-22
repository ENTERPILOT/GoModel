# ADR-0008: Virtual Models (unify aliases and access overrides)

## Context

GoModel exposes two operator-defined ways to shape model routing:

- **Aliases** map a new, addressable name to one real model (`fast` ->
  `openai/gpt-4o`). They are resolved early, as a rewrite.
- **Access overrides** gate an existing, scoped selector (`/`, `provider/`,
  `model`, `provider/model`) by `user_paths`. They are enforced late, as an
  authorization decision on the already-resolved model.

These are stored in separate tables, served by separate services, and surfaced
by separate admin endpoints, yet they are the same operator concept: a model
the operator defines rather than one a provider advertises. The split
duplicated `user_path` scoping and the database-migration handling, which caused
real bugs (an alias `user_paths` feature that re-implemented matching the
overrides already had, and shipped without a migration, breaking existing
databases).

We also want **load balancing** — one name resolving to several real models,
chosen per request — and there is no home for it today.

## Decision

Introduce one entity, the **virtual model**, persisted in `virtual_models` and
keyed uniquely by `source`.

- A row with `targets` is a **redirect**: `source` is a new name that rewrites
  to a real model. One target is an alias; many targets (a future, additive
  feature) are load balancing.
- A row without `targets` is an **access policy**: `source` is a scoped
  selector over existing models, gated by `user_paths`.

Behavior is **derived from the presence of `targets`** — there is no `role`
column. Storage, the service object, the admin API, and the dashboard are
unified, but resolution stays **staged**: redirect runs early, the access gate
runs late, exactly as before.

Pricing overrides remain a separate subsystem.

Version 1 preserves today's behavior exactly. The fields that enable load
balancing (`targets` beyond one, `strategy`, per-target `weight`) and scoped
redirects (`user_paths` on a redirect row) are stored but inert; they are
turned on by later changes that need no migration.

## Resolution Rule

1. **Resolver (early).** If the requested model exactly matches a redirect
   row's `source`, rewrite it to that row's single target.
2. **Authorizer (late).** Scope-match the resolved selector against the policy
   rows and enforce `user_paths`.

Redirect and policy rows never cross stages: a redirect `source` is a new name
that does not scope-match a real model, and a policy row has no target, so the
resolver ignores it.

## Migration

A one-time, idempotent seed copies existing `aliases` rows (as redirects) and
`model_overrides` rows (as policies) into `virtual_models` on first start when
the table is empty. The legacy tables are left intact for one release for
rollback; a later cleanup milestone removes the seed, the legacy packages, and
the legacy tables.

## Consequences

### Positive

- One `user_path` scope, one migration path, one admin surface, one UI.
- Load balancing becomes an additive change (data + a picker), not a third
  subsystem.
- Less duplicated code; the class of bug from divergent re-implementations is
  removed.

### Negative

- One table feeds two pipeline stages, mitigated by two independent in-memory
  indexes and by porting the existing, tested matching logic verbatim.
- `source` is a single namespace, so a redirect and a policy cannot share a
  name. This is structurally rare (aliases already forbid masking real models)
  and is accepted.
- Rollback is lossless only before the first virtual-model edit, because new
  writes go only to `virtual_models`.

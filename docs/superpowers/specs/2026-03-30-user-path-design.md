# User Path Tracking Design

**Date:** 2026-03-30

## Goal

Add first-class support for a canonical request user path sourced from
`X-GoModel-User-Path`, persist it in audit and usage records, expose filtering by
that path, and extend execution plans so requests can match path-scoped plans
with hierarchical fallback.

## Source Of Truth

- The only source in this slice is the inbound `X-GoModel-User-Path` header.
- Future derivation from API keys must reuse the same normalization and request
  context plumbing.
- OpenAI-compatible request bodies are not extended in this slice.

## Canonical Model

The stored/admin/runtime field name is `user_path`.

Normalization rules:

- trim surrounding whitespace
- empty string means "no user path"
- prepend `/` when missing
- collapse repeated `/`
- remove trailing `/` except for `/`
- reject `.` and `..` segments

Examples:

- `team/a/b/` -> `/team/a/b`
- ` /team//a///b ` -> `/team/a/b`
- `/` -> `/`
- empty header -> unset

## Execution Plan Scope

Execution plans gain an optional `scope_user_path` field.

Supported scope shapes in this slice:

- global
- provider
- provider + model
- user_path
- provider + user_path
- provider + model + user_path

Matching order is deterministic and most-specific-wins:

1. provider + model + deepest matching user path
2. provider + model
3. provider + deepest matching user path
4. deepest matching user path
5. provider
6. global

For a request with `/team/a/user`, the fallback path chain is:

- `/team/a/user`
- `/team/a`
- `/team`
- `/`

If no header is present, path-scoped plans are ignored.

## Persistence

Add nullable/indexed `user_path` columns/fields to:

- `audit_logs`
- `usage`

Keep the existing audit-log transport `path` field unchanged. `path` remains the
HTTP request path, while `user_path` is the business hierarchy from the header.

## Filtering Semantics

Admin filters on `user_path` are subtree filters:

- filter `/team` matches `/team`
- filter `/team` matches `/team/a`
- filter `/team` does not match `/team-b`
- filter `/` matches `/`
- filter `/` matches the entire hierarchy, including descendants such as `/team`
  and `/team/a`

This applies to:

- usage summary
- usage daily
- usage by model
- usage log
- audit log

Applying `user_path=/` in admin filters therefore means "all users/paths" for
those reports. SQL and MongoDB implementations must treat `/` as the full
subtree, not only the exact root row.

Examples:

- admin filter `/team` returns `/team`, `/team/a`, `/team/a/user`
- admin filter `/` returns `/`, `/team`, `/team/a`, `/team/a/user`

## Request Plumbing

Normalize once near ingress and expose the canonical value through the request
snapshot/runtime context. Downstream packages should consume the canonical value
instead of reading the header directly.

That keeps future API-key derivation behind a single seam.

## Testing

- unit tests for normalization and ancestor generation
- execution-plan matching tests for new scope precedence
- store/reader tests for persistence and subtree filters
- handler tests for admin query parsing
- integration tests for audit and usage capture on real requests
- streaming usage tests to ensure `user_path` survives the streaming path

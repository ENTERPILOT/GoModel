# ADR-0015: JSON Library Policy

## Status

Accepted

## Context

GoModel uses three JSON libraries, plus `gopkg.in/yaml.v3` for `config.yaml`:

- `github.com/goccy/go-json` for marshalling and unmarshalling, including
  echo's response serializer (`internal/server/json_serializer.go`).
- `github.com/tidwall/gjson` for read-only path queries over raw bytes.
- `encoding/json` where stdlib semantics are needed.

Each does a job the others do not. goccy is faster on the hot path, and the
pre-commit alloc check guards it. gjson answers a path query without
unmarshalling, which goccy has no API for. Stdlib cannot be dropped anyway:
echo's binder uses it, and `config/merge.go` relies on `json.NewDecoder` to
reject trailing data.

The rule for choosing between them was never written down, so new code picked
whichever library the neighbouring file happened to use. That left the same
`[]string` column helper in three places across two libraries.

## Decision

1. **goccy for marshal and unmarshal.** Import it as `json`:
   `"github.com/goccy/go-json"`. This is the default for new code.
2. **gjson for read-only extraction from raw bytes**: peeking at one field,
   detecting a shape, building a cache key, or pulling a message out of an
   upstream error body, without decoding the document.
3. **`encoding/json` only where its exact semantics are required.** Each such
   site says which semantics and why in a comment next to the import. Current
   examples:
   - `internal/auditlog/body.go` uses stdlib `Valid`, an allocation-free scan;
     goccy's `Valid` decodes the document.
   - `internal/server/messages_native.go` splices by the decoder's
     `InputOffset`, whose position stdlib documents precisely.
   - `config/merge.go` uses `json.NewDecoder` to reject trailing data.
   - Code that exchanges values with an API built on stdlib types (echo's
     binder, the MCP SDK, the public `pluginapi` package and the built-in
     plugins on it) may use stdlib to match it.
4. **Shared helpers over local copies.** JSON column encoding for SQL stores
   lives in `internal/storage/sqlutil` (`JSONStrings`, `DecodeJSONStrings`,
   `NullableJSONStrings`, `StringsFromJSON`).

Mixing two libraries in one file is allowed when each has a stated job; alias
the stdlib import (for example `encjson` or `stdjson`) so the default `json`
name stays goccy.

## Consequences

- Reviewers have one rule to check a new import against.
- Existing stdlib sites without a comment are not wrong, but should gain one
  when next touched.
- No dependency is removed. The indirect JSON and YAML modules in `go.mod`
  come from the MCP SDK, swaggo, and testify.

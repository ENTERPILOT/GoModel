# ADR-0014: Secret References

## Status

Accepted

## Context

Every credential GoModel uses arrives as a literal value: provider `api_key`,
Vertex service-account JSON, `proxy_url` user info, `server.master_key`,
storage DSNs, MCP headers, guardrail plugin secrets, vector-store keys, and
OpenTelemetry headers. They come from YAML, environment variables, or the
dashboard, and the dashboard-managed ones are written to the database in
plaintext (`provider_credentials`, `mcp_servers.headers`,
`guardrail_definitions`). `pluginapi.InputSecret` even documents "stored
encrypted", which was never true.

Operators who keep credentials in a secret manager have two workarounds today:
inject environment variables before the process starts, or render a config
file. Both copy the secret into a second place, neither picks up a rotated
value without a restart, and neither helps dashboard-managed credentials.

The `${VAR}` / `${VAR:-default}` interpolation that already exists runs over
the raw YAML text before decoding. It cannot carry a value with newlines or
quotes (a service-account JSON breaks the YAML), it leaves an unset variable
as a literal `${VAR}` that provider code then has to recognise and drop, and
its lookup is hard-wired to `os.Getenv`.

Other gateways converge on the same split: references to environment
variables and local encryption at rest are free, and connectors to external
secret managers are a commercial add-on (Kong vaults, Bifrost secret
management, LiteLLM secret managers). GoModel Pro ships those connectors as
its `vaults` feature. Core needs the seam they plug into, and the parts that
are plain security hygiene belong in core for everyone.

## Decision

### 1. One reference syntax: `${scheme:reference}`

A secret reference is `${<scheme>:<reference>}`, where `scheme` matches
`[a-z][a-z0-9+.-]*` and the reference does not start with `-`. The `-` rule
keeps the existing `${VAR:-default}` form unambiguous. The syntax follows the
OpenTelemetry Collector's configuration providers (`${env:NAME}`,
`${file:/path}`), extends the `${VAR}` form GoModel users already know, and
is plain YAML: unlike Kong's `{vault://...}` it does not open a flow mapping.

A reference may sit inside a larger value, so the two common composite
credentials work without a helper field:

```yaml
proxy_url: http://gomodel:${file:/run/secrets/proxy-pass}@proxy:3128
mcp:
  servers:
    github:
      headers:
        Authorization: Bearer ${env:GITHUB_TOKEN}
```

`$${` is an escape for a literal `${`. A resolved value is never scanned for
further references, so a secret that happens to contain `${...}` is used as-is
and cannot trigger a second lookup.

### 2. Two built-in schemes

| Scheme | Reference | Value |
| --- | --- | --- |
| `env` | variable name | the variable's value; unset or empty is an error |
| `file` | absolute path | the file's contents, one trailing `\n` or `\r\n` removed; at most 1 MiB |

`file` covers Docker and Kubernetes secret mounts, which previously needed a
wrapper script. `env` is equivalent to `${NAME}` in YAML, but it is strict
(an unset variable fails instead of leaving a literal) and, unlike `${NAME}`,
it also works in values stored through the dashboard.

Both are reserved: an extension cannot replace them.

### 3. Resolution happens per field, after decoding

The text-level `${VAR}` expansion skips anything that parses as a secret
reference. References are resolved later, field by field, by
`config.Secrets`, so a resolved value is never pasted back into YAML text and
can contain any bytes.

`config.LoadResult` carries the generation's resolver:

```go
// SecretResolver resolves the reference part of ${scheme:reference}.
type SecretResolver interface {
    ResolveSecret(ctx context.Context, reference string) (string, error)
}

func (s *Secrets) Register(scheme string, r SecretResolver) error // env and file are reserved
func (s *Secrets) Resolve(ctx context.Context, value string) (string, error)
func (s *Secrets) HasReference(value string) bool
func (s *Secrets) NotifyChanged() // see section 5
```

The resolver lives on `LoadResult` rather than on `ext.Registry` because it
is scoped to one configuration generation and has to work before `app.New`.
That includes configuring the extension that provides a scheme: Pro reads its
`extensions.vaults` section, which may itself use `${env:...}` or
`${file:...}`, before it can register `vault`.

Resolution order for one generation:

1. `config.Load` decodes YAML and applies the environment overlay. Secret
   references are left in place.
2. The distribution's `SetupConfig` / `ReloadConfig` hook runs. It may
   `Register` more schemes. `LoadResult.DecodeExtension` resolves references
   in the decoded section with the schemes registered at that moment, so an
   extension's own section can use them.
3. `run` resolves every string in `Config` (except `extensions`, which are
   decoded on demand) and `RawProviders`.
4. `providers.Init` resolves references that arrive through provider
   environment variables (`OPENAI_API_KEY=${vault:prod/llm#openai}`), which
   are merged after the hook.

Any reference that is still unresolved after step 3 or 4 stops the
generation with an error naming the field and the scheme, never the value.
The error message for an unknown scheme says which extension usually
provides it. A first start fails, and a reload keeps the running generation.
A literal `${vault:...}` string is therefore never sent upstream as a
credential. This replaces the old silent dropping of unresolved `${`
provider values for references. Plain `${VAR}` keeps its historical
behaviour.

### 4. Dashboard-managed entities accept references

Provider credentials, MCP server headers and environment, and guardrail
`InputSecret` fields accept references when they are saved. The admin API
stores the reference and returns it unmasked: a reference names where a
secret lives and is not itself secret. Literal values keep today's masking
and merge-on-PUT behaviour. The entity is resolved when the service builds
it. A reference that cannot be resolved at save time is rejected with a
validation error.

An extension may also implement an optional writer on its resolver:

```go
type SecretWriter interface {
    WriteSecret(ctx context.Context, key SecretKey, value string) (reference string, err error)
    DeleteSecret(ctx context.Context, reference string) error
}
```

When a writer is registered and enabled, a literal secret saved through the
dashboard is written to the external store and only the returned reference
is persisted. Core ships no writer.

### 5. Rotation without restart

`Secrets` remembers which field each reference resolved into, and a keyed
fingerprint of the value (never the value itself). `NotifyChanged` (called
by an extension when its backend reports a new version) re-resolves every recorded reference and compares fingerprints:

- When only provider API keys changed, the affected providers' keyrings are
  swapped in place. `Keyring` gains `Replace`, an atomic swap. Requests in
  flight finish on the key they started with.
- When any other field changed (DSNs, master key, service-account JSON,
  proxy URLs), core starts the same in-process generation reload that
  `SIGHUP` triggers. A failed reload keeps the running generation, exactly
  as today.
- Dashboard-managed entities re-resolve their own references and reinstall
  the affected provider, MCP server, or guardrail through the existing
  install path.

Rotating `server.master_key` also changes the derived anonymous install ID,
as it does today.

### 6. Encryption at rest for dashboard-managed secrets

Secret fields of dashboard-managed entities are encrypted with AES-256-GCM
when `GOMODEL_ENCRYPTION_KEY` is set. Encryption uses an envelope:

- Each database has one random 256-bit data key (DEK), stored wrapped in an
  `encryption_keys` table.
- The key-encryption key (KEK) is derived from `GOMODEL_ENCRYPTION_KEY` with
  Argon2id. The variable accepts any string; 32 random bytes in base64 is
  the documented recommendation.
- Field values are stored as `enc:v1:<key-id>:<base64(nonce||ciphertext)>`,
  with the entity id and field name as additional authenticated data, so a
  ciphertext cannot be moved to another row.
- Rotating the KEK re-wraps only the DEK. Existing rows stay readable during
  a DEK rotation because each value names its key.

Rows written before encryption was enabled are read as plaintext and
encrypted on their next write. `gomodel secrets reencrypt` encrypts
everything in one pass. Without a key, behaviour is unchanged. The startup
log warns once when dashboard-managed secrets exist in plaintext.

Extensions can replace the local KEK through `LoadResult`:

```go
type KeyWrapper interface {
    ID() string
    WrapKey(ctx context.Context, dek []byte) ([]byte, error)
    UnwrapKey(ctx context.Context, wrapped []byte) ([]byte, error)
}
```

GoModel Pro uses this for KMS-backed wrapping (AWS KMS, GCP KMS, Azure Key
Vault keys, Vault Transit). The incorrect "stored encrypted" claim in
`pluginapi.InputSecret` is corrected to describe this behaviour.

## Consequences

- One syntax covers YAML, environment variables, and dashboard input, with
  composite values allowed. Existing `${VAR}` configurations behave as
  before.
- Core no longer forwards unresolved references upstream. Configurations
  that relied on a provider with an unset `${VAR}` being dropped keep working,
  because plain `${VAR}` is unchanged. Only the new reference form fails
  closed.
- Rotated secrets reach running providers without a restart. API keys swap in
  place; everything else costs one generation reload.
- Database dumps no longer contain dashboard-managed credentials in plaintext
  once an encryption key is set. Losing the key makes those fields
  unrecoverable, which the documentation states plainly.
- The open-core boundary is explicit: references, `env`, `file`, rotation,
  and local encryption are core. External secret managers, write-back, and
  KMS key wrapping are GoModel Pro, plugged in through `Secrets.Register`,
  `SecretWriter`, and `KeyWrapper`.
- Resolved values live in process memory for the life of a generation. Go
  gives no reliable way to zero them, and this ADR does not claim otherwise.

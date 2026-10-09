# ADR-0014: Secret References

## Status

Accepted. Sections 1–3 and 5 are implemented; sections 4 and 6 are planned
and land in separate pull requests.

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
`[a-z][a-z0-9+.-]*` and the reference is non-empty, does not start with `-`,
and contains no `{` or `}`. The `-` rule keeps the existing `${VAR:-default}`
form unambiguous. The syntax follows the
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
reference, and leaves `$$` alone so the `$${` escape survives to the field
pass. References are resolved later, field by field, by
`config.Secrets`, so a resolved value is never pasted back into YAML text and
can contain any bytes.

`config.LoadResult` carries the generation's resolver:

```go
// SecretResolver resolves the reference part of ${scheme:reference}.
type SecretResolver interface {
    ResolveSecret(ctx context.Context, reference string) (string, error)
}
type SecretResolverFunc func(ctx context.Context, reference string) (string, error)

func NewSecrets() *Secrets // built-in schemes only; a nil *Secrets behaves the same
func (s *Secrets) Register(scheme string, r SecretResolver) error // env and file are reserved
func (s *Secrets) Resolve(ctx context.Context, value string) (string, error)
func (s *Secrets) HasReference(value string) bool
func (s *Secrets) ResolveFields(ctx context.Context, path string, target any) error
func HasSecretReference(value string) bool
func (s *Secrets) NotifyChanged() // see section 5
// Core-side rotation plumbing, also section 5: Changes, Recheck returning a
// *SecretRecheck (Fields, Value, Commit), and SecretNotifier with SetNotifier.

// The field being resolved ("server.master_key", "providers.openai.api_key",
// or "OPENAI_API_KEY_2" for an env-provided key) is on the context a resolver
// receives. Field paths are not secret;
// resolvers may audit-log them.
func SecretFieldFromContext(ctx context.Context) (string, bool)

// LoadResult.Secrets is set by Load. ResolveSecrets is step 3 below; it runs
// once per generation, and a retry returns the first result, so a resolved
// value is never scanned again, even after a failure.
func (r *LoadResult) ResolveSecrets(ctx context.Context) error

// Resolution failures are *SecretError{Field, Scheme, Err}; an unknown scheme
// wraps ErrUnknownSecretScheme.
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
   decoded on demand, and `guardrails.rules[].config`, see below). A
   load-time check that has to accept a reference, such as the MCP server URL
   scheme, is repeated on the resolved value.

   Guardrail rules are seeded into the guardrail store, so resolving their
   plugin config here would write resolved secrets to the database. Their
   `config` block is tagged `secrets:"deferred"` and skipped: the seeder
   resolves the fields that are not `InputSecret` fields of the plugin
   (`guardrails.rules[<i>].config.<key>`), and stores the secret fields with
   their references, which the guardrail service resolves once when it builds
   the instance (section 4). No other configuration section is copied into a
   store with secret values: MCP servers, users, tagging rules, and virtual
   models stay in memory, and the seeded rate-limit and budget rules hold no
   secrets.
4. `providers.Init` merges the provider environment variables
   (`OPENAI_API_KEY=${vault:prod/llm#openai}`) into `RawProviders` and then
   resolves the providers that pass the credential filter. A `config.yaml`
   value that an environment variable replaces, an environment variable the
   merge ignores, and a provider skipped for missing credentials are never
   looked up. Resolved values are data: a secret containing `${` is kept.
   API keys keep their source through key normalization: each key is
   resolved and reported under its `config.yaml` path
   (`providers.openai.api_keys[1]`) or the name of the environment variable
   that set it (`OPENAI_API_KEY_2`), even when two sources hold the same
   reference, and keys that resolve to the same value are collapsed
   afterwards. Provider settings that are parsed or steer the merge or the
   credential filter (models, model filters, booleans, `type`, `backend`,
   Vertex `auth_type`) are resolved, once, just before they are read; an
   environment setting is resolved only when the merge applies it.

`config.Load` parses some environment variables into numbers, booleans,
durations, and lists before any extension can register a scheme. A
reference in one of those is rejected with an error naming the variable;
string settings, and string values inside JSON variables, accept references.

Any reference that is still unresolved after step 3 or 4 stops the
generation with an error naming the field and the scheme, never the value.
The error message for an unknown scheme says which extension usually
provides it. A scheme GoModel knows nothing of (not built in, registered,
or hinted) is not repeated: its name is text taken from the value, which
may be a literal secret that only looks like a reference. A first start fails, and a reload keeps the running generation.
A literal `${vault:...}` string is therefore never sent upstream as a
credential. This replaces the old silent dropping of unresolved `${`
provider values for references. Plain `${VAR}` keeps its historical
behaviour.

### 4. Dashboard-managed entities accept references

Provider credentials (API keys, service-account JSON and base64 JSON, proxy
URL), MCP server headers, and guardrail `InputSecret` fields accept
references when they are saved. Admin-managed MCP servers have no
environment: stdio servers are declarative only, and their `env` values are
configuration, resolved in step 3. The admin API stores the reference and
returns it unmasked: a reference names where a secret lives and is not itself
secret. Only a value made of references alone is unmasked (an MCP header may
also start with an HTTP auth scheme, `Bearer ${env:TOKEN}`, and a proxy URL is
shown when its password is made of references); a value that mixes literal
text with a reference may carry a literal secret and is masked like one. Literal values keep today's masking and merge-on-PUT behaviour; the
mask sent back keeps the stored value, literal or reference. The database
holds the reference as typed; references are resolved by the service when it
builds the provider, MCP connection, or guardrail instance, never in the
store layer. A reference that cannot be resolved at save time is rejected
with a validation error naming the field and scheme.

A reference resolves on the gateway host, so whoever adds one can read any
environment variable or file the gateway can, the master key included. When a
master key is configured, only it may add or change a reference: for every
other credential, authentication marks the request with
`config.RestrictSecretReferences`, and `Secrets.StoreSecret` then rejects any
reference the entity's stored row does not already hold with a 403
(`secret_reference_requires_master_key`). Literals, the mask, and unchanged
references still save. Repointing an entity sends it what its references
resolve to, so `config.CheckSecretDestinations` refuses the same credentials
a change to a destination field (MCP `url`; provider `type`, `base_url`,
`backend`, `proxy_url`; guardrail type and plugin fields marked
`pluginapi.Field.Destination`) while the saved entity holds a reference.

Resolvers see field paths rooted at the entity: `provider_credentials.<name>.api_keys[<i>]`
(also `.service_account_json`, `.service_account_json_base64`, `.proxy_url`),
`mcp_servers.<slug>.headers.<Header>`, and
`guardrail_definitions.<name>.config.<key>`. The guardrail root is not
`guardrails` so it cannot collide with the `guardrails` section of
`config.yaml`.

These paths are deliberately not the identifiers encryption at rest
(section 6) binds ciphertexts to. Its additional authenticated data uses the
normalized credential name (`normalizeCredentialName`) and the field
`api_keys` for every key, so reordering a credential's keys never breaks
decryption. Reference paths use the name as saved and index each key
(`api_keys[<i>]`), so rotation reports the key that changed. Do not unify the
two.

`Secrets.ResolveEntity` resolves one entity's fields as a unit and records
them for rotation (section 5) only once the entity is installed, replacing
whatever that entity recorded before; `Secrets.ForgetEntity` drops the
records of a deleted entity.

A stored entity whose references fail when it is loaded is handled like any
other invalid stored row of its kind: a provider credential or MCP server is
logged and skipped, and a running MCP server keeps its current headers;
guardrails load as a set, so a failure fails the guardrail load (startup
fails, a reload keeps the running generation, a periodic refresh keeps the
current instances), which keeps guardrails failing closed.

An extension may also register an optional writer, per generation like
`Register`, with `Secrets.SetWriter`:

```go
type SecretKey struct{ Entity, ID, Field string } // "provider_credentials", "openai", "api_keys[0]"

type SecretWriter interface {
    WriteSecret(ctx context.Context, key SecretKey, value string) (reference string, err error)
    DeleteSecret(ctx context.Context, reference string) error
    // OwnsReference reports whether WriteSecret created reference, so core
    // never deletes a secret an operator referenced by hand.
    OwnsReference(reference string) bool
}
```

When a writer is registered, a literal secret saved through the admin API is
written to the external store and only the returned reference is persisted.
The writer receives the value the field would resolve to without one (each
`$${` escape already turned into `${`) and must return exactly one
`${scheme:reference}` with nothing around it. `WriteSecret` must create a new secret and return a distinct reference
each time, never changing what an existing reference resolves to, because a
save can still fail after the write.
When an entity is deleted, or a field's owned reference is replaced, core
calls `DeleteSecret` for each reference the writer owns, after the database
commit, best effort (a failure is logged). A save that fails after the write
deletes what it wrote. A save is rejected when it holds a reference the
writer owns that the entity's stored row does not: one read before the row was
replaced or deleted, or copied from another entity, may name a deleted secret.
Saves and deletes of one entity kind are serialized from reading the stored
row through deleting the secrets it held. Core ships no writer.

### 5. Rotation without restart

`Secrets` remembers each field a reference resolved into: its path, the value
as configured, and an HMAC-SHA256 fingerprint of the resolved value under a
random per-process key (never the value itself). Fields are recorded under
the label resolution reports them by (`server.master_key`,
`extensions.vaults.token`). A provider API key is recorded under the source
the operator wrote: its `config.yaml` path (`providers.openai.api_keys[1]`) or
the environment variable that set it (`OPENAI_API_KEY_2`). Providers dropped
for missing credentials are never resolved, so they are not tracked.

`NotifyChanged`, called by an extension when its backend reports a new
version, never blocks, and calls that arrive before the check runs coalesce
into one. Core then re-resolves every recorded reference with the same
resolvers and compares fingerprints:

- When only provider API keys changed, the changed keys are patched by
  source, key de-duplication and credential filtering run again, exactly as
  at startup, and the affected providers' keyrings are swapped in place.
  `Keyring` gains `Replace`, an atomic swap of an immutable key set. Requests
  in flight finish on the key they started with. Session stickiness is rendezvous hashing over
  the key set, so sessions on a removed key move, and a new key takes its
  share of the rest.
- When any other field changed (DSNs, master key, service-account JSON,
  proxy URLs), a provider would lose its last key, or the changed key is also
  copied elsewhere (the semantic cache embedder), core starts the same
  in-process generation reload that `SIGHUP` triggers. A failed reload keeps
  the running generation, exactly as today, and the change stays pending, so
  the next notification retries it.
- When a reference cannot be re-resolved, core logs a warning naming the
  field and scheme, keeps every current value, and does not reload. A failed
  lookup is not evidence that the running credential is stale; the extension
  that notified decides that.
- Dashboard-managed entities re-resolve their own references and reinstall
  the affected provider, MCP server, or guardrail through the existing
  install path: a provider whose API keys alone changed has its keyring
  swapped, any other provider change rebuilds that provider, an MCP server is
  redialed, and a guardrail instance is rebuilt and the workflows recompiled.
  Their fields never trigger a generation reload. An entity whose references
  cannot be re-resolved keeps its current values, and its change stays
  pending. A failed entity reference does not hold back other entities or
  the configuration; a failed configuration reference holds back only the
  configuration's own swap or reload.

Each change is logged at info with the action and the field paths, never
values.

Every generation has its own `Secrets`, and an extension holds the one it saw
last, which may belong to a reload that was rejected. So the process shares
one `SecretNotifier` between all generations' `Secrets`, and only the
generation that is serving listens: its watcher starts when its server starts
and stops with it. A notification sent through any generation's `Secrets`
reaches the serving one, which re-checks with its own `Secrets`. Stopping a
watcher waits only briefly for a resolver; a check that outlives its
generation takes no action and passes the notification on to the next one. `env` and
`file` references are not polled; they rotate with a reload.

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

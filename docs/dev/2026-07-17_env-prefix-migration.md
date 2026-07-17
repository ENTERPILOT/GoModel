# Environment variable prefix migration

Status: mechanism shipped; documentation rename pending.

GoModel-defined environment variables are canonically spelled `GOMODEL_<NAME>`.
The unprefixed spelling still resolves but is deprecated and warns once per
variable at startup.

## Why

The environment namespace is global and flat — it is shared with every other
process in the container. Names like `BASE_PATH`, `HTTP_TIMEOUT`,
`STORAGE_TYPE`, and `LOGGING_ENABLED` are land grabs on generic names that say
nothing about who owns them, and they collide the first time an operator shares
an `env_file` across Compose services or an `envFrom` ConfigMap across
containers.

The convention this follows is the common one: **prefix everything you own; use
a bare name only when deliberately joining a convention someone else defined.**
WordPress (`WORDPRESS_DB_HOST`), Apache (`APACHE_RUN_USER`), Grafana
(`GF_SERVER_HTTP_PORT`), Vault (`VAULT_ADDR`), MinIO (`MINIO_ROOT_USER`), and
Ollama (`OLLAMA_HOST`) all do this. LiteLLM makes the same two-tier split
GoModel makes here: `LITELLM_MASTER_KEY` for its own config, bare
`OPENAI_API_KEY` for the ecosystem.

`GOMODEL_MASTER_KEY` and `GOMODEL_CACHE_DIR` already followed the convention.
"Everything bare except two" was the worst state: readers could not infer the
rule either way.

## Resolution rules

Implemented in `internal/envcompat`:

1. A **value with non-whitespace content wins, canonical first**.
   `GOMODEL_SQLITE_PATH` beats `SQLITE_PATH`.
2. A **blank canonical does not shadow a working legacy value**. A Compose file
   with an unexpanded `GOMODEL_SQLITE_PATH=` (or a quoted-but-unexpanded
   `GOMODEL_SQLITE_PATH=" "`) alongside a real `SQLITE_PATH` resolves to the
   real one instead of silently discarding it.
3. Presence is still reported when only blank values are set, so callers that
   use the `ok` bool to detect an explicit `""` keep working
   (`RESPONSE_CACHE_SIMPLE_ENABLED=` means "off", not "unset").
4. Reading a legacy spelling logs `WARN` once per variable, naming the
   replacement.
5. Exempt names and names already carrying the prefix are read as given, never
   double-prefixed. Setting the prefixed spelling of an exempt name
   (`GOMODEL_PORT`) warns once that it is not read, so a mechanically-prefixed
   env block cannot become a silent misconfiguration.
6. `Scan` (the dynamic families) resolves every discovered suffix through the
   same `lookup` as `Lookup`, so the two cannot diverge, and each `Entry.Name`
   is the spelling that actually supplied the value — error messages name a
   variable that exists in the operator's environment.

## Exempt — these keep their bare names permanently

| Variable | Reason |
|---|---|
| `PORT` | PaaS platforms inject it (Railway, Heroku, Cloud Run). Honoring the bare name is required, not optional. |
| `REDIS_URL` | Railway/Heroku Redis plugins inject it. Bare = zero-config wiring. |
| `<PROVIDER>_API_KEY`, `<PROVIDER>_API_KEY_<n>`, `<PROVIDER>_BASE_URL`, `<PROVIDER>_MODELS`, `<PROVIDER>_API_VERSION` | The vendor's namespace, not ours. `OPENAI_API_KEY` and `OPENAI_BASE_URL` are the OpenAI SDK's own names — reading them bare is what makes GoModel drop-in. The family is generated from provider name + suffix, so it moves as one unit or not at all. |
| `DOCKER_HOST`, `MOCK_*`, `RECORD`, `UPDATE_GOLDEN`, `TEST_DATABASE_DSN`, `MONGO_TEST_DSN` | Test harness only; never documented, never read by a deployment. |

`POSTGRES_URL` and `MONGODB_URL` are **not** exempt despite looking like
ecosystem names: nothing injects them. The real PaaS convention is
`DATABASE_URL`, which GoModel does not read today — accepting it is a separate
feature, not a rename.

Note the deliberate split within the `REDIS_` family: `REDIS_URL` stays bare
(a platform hands it to you) while `REDIS_KEY_MODELS` / `REDIS_TTL_MODELS` /
`REDIS_KEY_RESPONSES` / `REDIS_TTL_RESPONSES` are prefixed (key names GoModel
invented). Principle over aesthetics.

Likewise, the `*_API_KEY` variables under `SEMANTIC_CACHE_` are prefixed: no
vendor SDK reads `SEMANTIC_CACHE_QDRANT_API_KEY`, so it is GoModel's name.

## Judgment calls

These sit near the provider family but configure GoModel behavior, not vendor
credentials, so they are prefixed:

| Variable | Note |
|---|---|
| `GOMODEL_USE_GOOGLE_GEMINI_NATIVE_API` | Routing flag, not a Google credential. |
| `GOMODEL_ANTHROPIC_DEFAULT_MAX_TOKENS` | Translation behavior, not Anthropic auth. Breaks visual symmetry with `ANTHROPIC_API_KEY`. |
| `GOMODEL_OPENCODE_GO_MESSAGES_MODELS` | Routing, but easily confused with the exempt `<PROVIDER>_MODELS` pattern. |
| `GOMODEL_OPENROUTER_SITE_URL`, `GOMODEL_OPENROUTER_APP_NAME` | Attribution config GoModel invents (OpenRouter's own mechanism is the `HTTP-Referer` / `X-Title` headers, not env vars). |

## Everything else

Every other GoModel-defined variable takes the prefix mechanically:
`GOMODEL_` + the existing name. That covers the struct-tagged config
(`GOMODEL_SQLITE_PATH`, `GOMODEL_LOGGING_ENABLED`, `GOMODEL_HTTP_TIMEOUT`, ...),
the named reads outside the tag walker (`GOMODEL_CONFIG_STRICT`,
`GOMODEL_VIRTUAL_MODELS`, `GOMODEL_MCP_SERVERS`, `GOMODEL_SEMANTIC_CACHE_*`,
`GOMODEL_RESPONSE_CACHE_SIMPLE_ENABLED`), the logging vars
(`GOMODEL_LOG_LEVEL`, `GOMODEL_LOG_FORMAT`), and the four dynamic families
(`GOMODEL_SET_BUDGET_<PATH>`, `GOMODEL_SET_RATE_LIMIT_<PATH>`,
`GOMODEL_SET_PROVIDER_RATE_LIMIT_<NAME>`, `GOMODEL_TAGGING_HEADER_<N>` and its
`_PREFIX` / `_DONOTPASS` / `_DELIMITER` companions).

## Implementation notes

- **Two mechanisms, not one.** Most variables resolve by exact lookup
  (`envcompat.Lookup` / `Get`), reached for the whole tagged config through the
  single `os.Getenv` call in `applyEnvOverridesValue`. The four dynamic families
  are discovered by walking `os.Environ()` and use `envcompat.Scan`, which
  matches both prefixes and de-duplicates by suffix.
- **`Scan` sorts by suffix.** Callers resolve suffixes to canonical keys and two
  suffixes can collide there, so iteration order decides which wins; sorting
  keeps that from depending on the order the OS returns.
- **Companions resolve through `Lookup`/`Get` by bare name**, which accepts
  either spelling. This is what lets a canonical `GOMODEL_TAGGING_HEADER_1`
  pair with a legacy `TAGGING_HEADER_1_PREFIX`. (`Entry.Name` itself is the
  spelling that supplied the value, reserved for messages.)
- **`GOMODEL_CONFIG_STRICT` is read before the env-tag walker** because it
  governs the YAML parse, so it calls `envcompat` at its own site.
- **`HTTP_TIMEOUT` / `HTTP_RESPONSE_HEADER_TIMEOUT` are read twice** — by the
  config struct tags and independently by `internal/httpclient`. Both go through
  `envcompat`, which is why the helper lives in `internal/` rather than
  `config/`.

## Remaining work

The mechanism is in place and both spellings work. Still to do:

1. Rename the variables throughout the documentation surface to the canonical
   spelling: `.env.template`, `config/config.example.yaml`, `README.md`,
   `CLAUDE.md`, `helm/`, `docker-compose.yaml`, and the benchmark compose files.
   Deliberately not done in the mechanism PR: it is a large mechanical diff, and
   the exempt rules above make a blind find-and-replace unsafe.
2. Decide on accepting bare `DATABASE_URL` for Postgres (PaaS interop win, new
   behavior rather than a rename).
3. Pick the release that removes the legacy spellings.

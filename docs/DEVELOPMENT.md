# Development

## Prerequisites

Install all required development tools in one step:

```bash
make install-tools
```

This installs:

- [golangci-lint v2](https://golangci-lint.run/welcome/install/) - required for `make lint`
- [pre-commit](https://pre-commit.com/) - required for git hook setup

After installing tools, set up the pre-commit hooks:

```bash
pre-commit install
```

## Testing

```bash
make test             # Go unit tests (plus the store suites, see below)
make test-race        # Go unit tests with race detection and coverage
make test-dashboard   # Dashboard JavaScript unit tests
make test-e2e         # End-to-end tests against in-process mock providers (-tags=e2e, no Docker)
make test-contract    # Replay recorded provider payloads through the adapters (-tags=contract)
make test-integration # Verify database state through a real gateway (-tags=integration, needs Docker)
make test-all         # Unit, dashboard, e2e, integration, and contract tests
make perf-check       # Allocation and voice-routing latency guards (tests/perf)
make test-live        # Call real providers; each test skips unless its API key is set
make fuzz             # Run the fuzz targets beyond their seed corpora (FUZZTIME=30s)
```

Tests assert with [testify](https://github.com/stretchr/testify). Handler tests build requests with `internal/echotest`, provider tests fake upstreams with `internal/providers/providertest`, and providers built on the shared OpenAI-compatible adapter run the `providertest.AssertChatCompatible` contract instead of repeating the same request tests. `make test` also runs `internal/testconventions`, which rejects hand-rolled `if cond { t.Fatal(...) }` assertions, keeps a per-file budget of `time.Sleep` calls outside `testing/synctest` bubbles that may only shrink, requires every registered provider to have a shared contract test or recorded replay fixtures, and fails a CI run that lost its PostgreSQL or MongoDB test database.

The contract replay suite and its golden files are described in `tests/contract/README.md`; the performance guards in `tests/perf/README.md`.

### Store suites on every backend

Every persistence domain (usage, audit log, budgets, rate limits, auth keys,
workflows, ...) ships a SQL store and a MongoDB store. Their unit suites are
written once against the domain's `Store` interface and run on each backend
that is reachable:

| Backend | Helper | How it is enabled |
|---|---|---|
| SQLite | `sqlxtest.Run` | always, in-memory |
| PostgreSQL | `sqlxtest.Run` | `GOMODEL_TEST_POSTGRES_URL` set (CI: `tools/ci/postgres.sh`) |
| MongoDB | `mongotest.Run` | `MONGO_TEST_DSN` set (CI: `tools/ci/mongo-replset.sh`) |

Without the variable the backend's subtest skips, so a green run without
PostgreSQL or MongoDB covers SQLite only. A variable that is set but points at
an unreachable server fails the run. CI sets both in the unit job. Locally:

```bash
# single-node replica set on 127.0.0.1:27017 (the guardrails and workflows
# stores use transactions, so a plain mongod is not enough)
sh tools/ci/mongo-replset.sh start && sh tools/ci/mongo-replset.sh wait
MONGO_TEST_DSN='mongodb://localhost:27017/?replicaSet=rs&directConnection=true' go test ./internal/...
docker rm -f gomodel-mongo
```

Set `MONGO_PORT` only when 27017 is already taken, for example by the
`docker compose` MongoDB, and use the same port in `MONGO_TEST_DSN`.

### Before a release

Two checks run by hand against real providers and real databases, so they are
not part of CI:

- `tests/e2e/release-e2e-scenarios.md` is the release curl matrix. Start the
  stack with `tests/e2e/manage-release-e2e-stack.sh start` and run it with
  `tests/e2e/run-release-e2e.sh`; both need provider credentials in `.env`.
- `tests/e2e/upgrade-compat.sh` boots the previous release against each
  database backend, then the working tree on the same data, and checks that
  every store still reads and writes. The store suites cannot catch this: they
  build a fresh schema per case and never meet a table an older release created.

## Linting

Requires [golangci-lint v2](https://golangci-lint.run/welcome/install/)

```bash
make lint          # Check code quality
make lint-fix      # Auto-fix issues
```

## Swagger and OpenAPI

Use `make swagger` to regenerate the checked-in Swagger docs package and
`docs/openapi.json`.

GoModel intentionally uses `github.com/swaggo/swag/v2/cmd/swag`. The Swagger UI
is served through Echo v5 and `github.com/swaggo/echo-swagger`'s
`WrapHandlerV3`, which reads registered specs through `github.com/swaggo/swag/v2`.
Using the v1 `swag` generator will produce a docs package that does not match
the Swagger build.

## Release Hygiene

Releases are generated automatically from merged PRs, categorized by labels and PR titles.

- PR titles are validated in CI using Conventional Commit format (`type(scope): summary`)
- Release labels are auto-applied from PR title type (`feat` -> feature, `fix` -> bug fix, etc.)
- Internal changes (`chore`, `ci`, `build`, `test`, most `refactor`) are excluded from release notes by default
- Prefer **Squash and merge** so each PR lands as one commit aligned with the PR title
- Release builds push the Docker image by digest first, promote the digest to semver tags and `latest` for stable releases, then publish the GitHub release
- If GitHub release publishing fails after Docker tag promotion, rerun the release workflow after fixing the failure or manually reconcile the promoted Docker tags
- If needed, apply `release:skip` on a PR to force exclusion from release notes

## Repomix

You can compress the whole repository for LLMs with the following command:

```
$ repomix -i "./*.md,./**/*_test.go,./tests/,./**/*.md,./.claude/,./data/,./docs/,./helm/,./.cache/,./.github/,./cmd/gomodel/docs/" --style=markdown --remove-comments
```

## Log output

Log format is chosen automatically based on the environment:

- **TTY** (interactive terminal): colorized, human-readable text via [tint](https://github.com/lmittmann/tint)
- **Non-TTY** (piped, redirected, Docker, CI): structured JSON

```text
12:12PM INFO  starting gomodel version=dev commit=none
12:12PM WARN  SECURITY WARNING: GOMODEL_MASTER_KEY not set ...
12:12PM INFO  starting server address=:8080
```

Override the auto-detection with `LOG_FORMAT`, and the verbosity with `LOG_LEVEL`:

| Value     | Effect                                          |
| --------- | ----------------------------------------------- |
| _(unset)_ | Auto-detect: text+colors on TTY, JSON otherwise |
| `text`    | Always text (no colors if not a TTY)            |
| `json`    | Always JSON, even on a TTY                      |

| `LOG_LEVEL` value | Effect                                       |
| ----------------- | -------------------------------------------- |
| `debug`           | Include debug, info, warn, and error logs    |
| `info`            | Include info, warn, and error logs (default) |
| `warn`            | Include only warn and error logs             |
| `error`           | Include only error logs                      |

```bash
LOG_FORMAT=text make run   # force text output
LOG_FORMAT=json make run   # force JSON output
LOG_LEVEL=debug make run   # include debug logs
```

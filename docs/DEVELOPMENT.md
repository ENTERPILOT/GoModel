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
make test            # Go unit tests
make test-race       # Go unit tests with race detection and coverage
make test-dashboard  # Dashboard JavaScript unit tests
make test-e2e        # End-to-end tests (requires -tags=e2e; uses in-process mock servers, no Docker)
make test-all        # All tests
```

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
$ repomix -i "./*.md,./**/*_test.go,./tests/,./**/*.md,./.claude/,./data/,./docs/,./deploy/,./.cache/,./.github/,./cmd/gomodel/docs/" --style=markdown --remove-comments
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

## Running locally

Several workflows are available depending on how close to production you want to be:

| Workflow | Command | Notes |
| --- | --- | --- |
| Bare process | `make run` | Fastest; SQLite + local file cache by default |
| Docker Compose (infra) | `make infra` | Redis, Postgres, MongoDB, Adminer |
| Docker Compose (full) | `make image` | App + Prometheus on top of infra |
| Local Kubernetes | `make kind-up && make dev-k8s` | kind + Skaffold + Helm chart |

### Local Kubernetes (kind + Skaffold)

Develop against a local kind cluster with a build → deploy → watch loop that uses
the production Helm chart:

```bash
make kind-up     # create the kind cluster + dependencies + mock LLM (one time)
make dev-k8s     # build, load into kind, deploy, watch (access on :8080)
make undeploy-k8s
make kind-down
```

`make kind-up` also deploys an in-cluster OpenAI-compatible mock upstream, so
`/v1/chat/completions` works end-to-end without real provider credentials.

See [dev/local-kubernetes.md](dev/local-kubernetes.md) for prerequisites,
configuration, and troubleshooting.

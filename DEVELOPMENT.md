# Development

## Prerequisites

Install all required development tools in one step:

```bash
make install-tools
```

This installs:

- [golangci-lint v2](https://golangci-lint.run/welcome/install/) — required for `make lint`
- [pre-commit](https://pre-commit.com/) — required for git hook setup

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

## Release Hygiene

Releases are generated automatically from merged PRs, categorized by labels and PR titles.

- PR titles are validated in CI using Conventional Commit format (`type(scope): summary`)
- Release labels are auto-applied from PR title type (`feat` -> feature, `fix` -> bug fix, etc.)
- Internal changes (`chore`, `ci`, `build`, `test`, most `refactor`) are excluded from release notes by default
- Prefer **Squash and merge** so each PR lands as one commit aligned with the PR title
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

| `LOG_LEVEL` value | Effect |
| ----------------- | ------ |
| `debug`           | Include debug, info, warn, and error logs |
| `info`            | Include info, warn, and error logs (default) |
| `warn`            | Include only warn and error logs |
| `error`           | Include only error logs |

```bash
LOG_FORMAT=text make run   # force text output
LOG_FORMAT=json make run   # force JSON output
LOG_LEVEL=debug make run   # include debug logs
```

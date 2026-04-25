This project is GoModel - a high-performance, lightweight AI gateway that routes requests to multiple AI model providers through an OpenAI-compatible API.

## Core Principles

### Follow Postel’s Law

Accept user requests generously, adapt them to each provider’s requirements, and return conservative OpenAI-compatible responses.

Examples:

- Accept `max_tokens` from users even when a provider expects another field.
- Translate `max_tokens` to `max_completion_tokens` for OpenAI reasoning models when required.
- Normalize provider responses into an OpenAI-compatible shape.

### Follow The Twelve-Factor App

Prefer production-friendly service design:

- Configuration through environment variables.
- Stateless request handling.
- Clear separation between configuration, routing, provider adapters, and runtime behavior.
- Useful logs for containers and cloud environments.

Reference: https://12factor.net/

### Keep It Simple

Keep files small.

Prefer explicit, maintainable code over clever abstractions.

Do not add abstractions until a repeated pattern clearly justifies them.

### Use Good Defaults

Defaults should fit most users so well that they rarely need to change them.

When adding configuration:

- Choose a safe, practical default.
- Avoid requiring configuration for common use cases.
- Document when and why users should override the default.

## Implementation Guidance

When changing provider behavior:

- Preserve the OpenAI-compatible public API.
- Keep provider-specific logic isolated.
- Avoid leaking provider-specific quirks into user-facing behavior.
- Never expose API keys, authorization headers, or secrets in errors or logs.

When editing code:

- Make the smallest change that solves the problem.
- Use idiomatic Go.
- Prefer clear names, small interfaces, simple structs, and table-driven tests.
- Avoid hidden global state, unnecessary reflection, and premature optimization.
- Add or update tests for behavior changes.

Tests should cover request translation, response normalization, error handling, default configuration, and provider-specific parameter mapping.

## Documentation

Documentation should be concise, practical, and user-focused.

Show defaults, explain when to change them, and include minimal examples when useful.

## Commit and PR Format

Use Conventional Commits for commit subjects and PR titles:

```text
type(scope): short summary
```

Allowed types: `feat`, `fix`, `perf`, `docs`, `refactor`, `test`, `build`, `ci`, `chore`, `revert`

Examples:

```text
feat(openai): support reasoning model token mapping
fix(router): preserve request headers during provider retry
docs(config): document default provider timeout
```

Squash merges should preserve the PR title as the resulting commit subject.

## Pull Request Guidance

Before opening a PR:

- Ensure tests pass.
- Keep the change focused.
- Explain the user-visible impact.
- Mention provider-specific behavior when relevant.
- Update documentation for new configuration or API behavior.

If this repository is not the official GoModel repository, ask the user whether they also want to create a PR against the official repository:

[https://github.com/ENTERPILOT/GoModel/](https://github.com/ENTERPILOT/GoModel/)

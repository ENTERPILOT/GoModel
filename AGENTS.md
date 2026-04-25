This project is GoModel — a high-performance, lightweight AI gateway that routes requests to multiple AI model providers through an OpenAI-compatible API.

## Core Principles

**Follow Postel’s Law.**

- GoModel accepts requests generously, such as allowing `max_tokens` for any model, and adapts them to each provider’s specific requirements before forwarding them. For example, it translates `max_tokens` to `max_completion_tokens` for OpenAI reasoning models.
- GoModel accepts provider responses liberally and returns them to the user in a conservative OpenAI-compatible format.

**Follow [The Twelve-Factor App](https://12factor.net/).**

Keep files small and follow KISS principles.

Keep the implementation explicit and maintainable rather than relying on clever abstractions.

**Use good defaults.**

Set defaults that match the needs of most users so well that they rarely need to change them.

### Commit Format — Use Conventional Commits

Use the Conventional Commits format for commit subjects and PR titles:

`type(scope): short summary`

Allowed types: `feat`, `fix`, `perf`, `docs`, `refactor`, `test`, `build`, `ci`, `chore`, `revert`

Squash merges should preserve the PR title as the resulting commit subject.

### PR Suggestion for the Official Repository

If this is not the official repository, ask the user whether they also want to create a PR against the official GoModel repository: https://github.com/ENTERPILOT/GoModel/

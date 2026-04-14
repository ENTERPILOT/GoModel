# GoModel - AI Gateway

## Purpose

GoModel is a high-performance AI gateway in Go that routes requests to multiple AI model providers through an OpenAI-compatible API.

## Core Principles

Follow Postel's Law: be conservative in what you send, liberal in what you accept.

- GoModel accepts client requests generously (e.g. `max_tokens` for any model) and adapts them to each provider's specific requirements before forwarding (e.g. translating `max_tokens` → `max_completion_tokens` for OpenAI reasoning models).
- GoModel accepts providers' responses liberally and passes them to the user in a conservative OpenAI-compatible shape.

[The Twelve-Factor App](https://12factor.net/).

Keep implementation explicit and maintainable over clever abstractions.

Backward compatibility is not a primary constraint in the current development stage.

## Quick Workflow

1. Make small, focused changes.
2. Run format/lint/tests relevant to the change.
3. Do not hide work in detached goroutines; respect context synchronously and return typed `core.GatewayError` values.

## Commit Format

Use Conventional Commit format for commit subjects and PR titles:

`type(scope): short summary`

Allowed types: feat, fix, perf, docs, refactor, test, build, ci, chore, revert

Squash merges should preserve the PR title as the resulting commit subject.

# GoModel - AI Gateway

## Purpose

GoModel is a high-performance AI gateway in Go that routes requests to multiple AI model providers through an OpenAI-compatible API.

## Core Principles

Follow Postel's Law: be conservative in what you send, liberal in what you accept.

- GOModel accepts client requests generously (e.g. `max_tokens` for any model) and adapts them to each provider's specific requirements before forwarding (e.g. translating `max_tokens` → `max_completion_tokens` for OpenAI reasoning models).
- GOModel accepts providers' responses liberally and passes them to the user in a conservative OpenAI-compatible shape.

[The Twelve-Factor App](https://12factor.net/).

Keep implementation explicit and maintainable over clever abstractions.

Backward compatibility is not a primary constraint in the current development stage.

## Quick Workflow

1. Make small, focused changes.
2. Run format/lint/tests relevant to the change.

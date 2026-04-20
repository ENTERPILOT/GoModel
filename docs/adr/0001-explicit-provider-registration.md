# ADR-0001: Explicit Provider Registration

## Context

GoModel supports multiple LLM providers, including OpenAI, Anthropic, Gemini, xAI, Groq, OpenRouter, Z.ai, Azure OpenAI, Oracle, Ollama, and custom OpenAI-compatible endpoints. Each provider must be registered with the factory before use.

## Decision

Use explicit registration in main.go:

- Don't use `init()` functions from provider packages
- Export `NewProviderFactory()` for creating factory instances
- Register providers explicitly: `factory.Add(openai.Registration)`

## Consequences

### Positive

- **Explicit control flow**: Registration order is visible and controllable
- **No global state**: Factory is created and passed explicitly
- **Better testability**: Tests create isolated factories without workarounds
- **IDE navigation**: Click through to Registration instead of dead-end blank imports
- **Conditional registration**: Easy to add feature flags for experimental providers
- **Go best practices**: Avoids init() side effects

### Negative

- Slightly more boilerplate in main.go (11 explicit registration calls)

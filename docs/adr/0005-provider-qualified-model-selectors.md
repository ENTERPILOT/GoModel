# ADR-0005: Provider-Qualified Model Selectors

## Context

GOModel supports multiple configured providers.

Some upstream model IDs also contain slashes, for example
`google/gemini-xyz`.

That creates one simple question:

- is `google/gemini-xyz` a raw model ID?
- or is `google` the provider and `gemini-xyz` the model?

We need one simple rule.

## Decision

Use two forms:

- `model`
- `provider/model`

### No slash

If the request is:

```json
{ "model": "gpt-5-nano" }
```

then GOModel treats it as an unqualified model lookup.

It checks the shared unqualified registry entry for `gpt-5-nano`.
If multiple providers expose that model, the first registered provider wins.

This is the existing fallback behavior for bare model names.

### One or more slashes

If the request has a slash, GOModel splits on the first slash only.

Examples:

- `gemini/gemini-xyz` means provider `gemini`, raw model `gemini-xyz`
- `openrouter/google/gemini-xyz` means provider `openrouter`, raw model
  `google/gemini-xyz`

The key rule is:

- if the first segment is a configured provider name, treat it as
  `provider/model`
- if the first segment is not a configured provider name, treat the full string
  as a raw model ID

## Resolution Rule

Resolution is:

1. If the selector has no slash, use the unqualified lookup.
2. If the selector has a slash and the prefix is a configured provider, use
   provider-qualified lookup.
3. If the selector has a slash and the prefix is not a configured provider,
   fall back to the full raw model ID.

## Conflict Cases

If both of these exist:

- provider `gemini` with raw model `gemini-xyz`
- some provider with raw model `gemini/gemini-xyz`

then:

- `gemini/gemini-xyz` resolves to provider `gemini` plus raw model
  `gemini-xyz`
- the raw model `gemini/gemini-xyz` loses, because `gemini` is a configured
  provider name

If the request is just:

- `gpt-5-nano`

and multiple providers expose it, the first registered provider wins.

## Consequences

### Positive

- `/v1/models` can expose exact public selectors
- provider-qualified routing is deterministic
- bare model names still work
- slash-shaped raw model IDs still work when their prefix is not a provider

### Negative

- raw model IDs containing slashes can be shadowed by provider-qualified
  selectors
- bare model names remain ambiguous when multiple providers expose the same
  model, so unqualified lookup is still first-provider-wins

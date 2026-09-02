# ADR-0011: Field Forwarding on Translated Paths

## Status

Accepted

## Context

GoModel exposes an OpenAI-compatible API (`/v1/*`) and an Anthropic Messages
ingress, and routes each request to a provider whose native API may differ
from what the client sent. ADR-0002 already decided that unknown JSON fields
are preserved rather than dropped, and `ExtraFields` on the request types
implements that for requests.

What happens to a field the gateway *does* know about, when the selected
provider or model cannot take it, was never written down. The adapters answer
the same question three different ways today:

- The OpenAI adapter maps `max_tokens` to `max_completion_tokens` and drops
  `temperature` for reasoning models (o-series, GPT-5).
- The shared cache-control helper strips Anthropic `cache_control` directives
  before a request goes to a provider that does not accept them.
- The Anthropic adapter rejects `response_format` and `verbosity` with a 400
  instead of dropping or translating them.

Each answer is reasonable on its own. Without a shared rule the next adapter
picks one at random, and clients cannot predict whether a field will be
honored, ignored, or refused.

## Decision

On translated paths, a field the client sends is handled by the first rule
that matches, in this order:

1. **Unknown to the gateway.** Forward untouched, in both directions. This is
   the ADR-0002 forward-compatibility promise and is how clients reach
   provider-native extras (for example OpenRouter routing preferences) without
   waiting for a GoModel release.
2. **Known, and the target accepts it.** Forward as-is.
3. **Known, and the target needs a different shape.** Translate. The client
   asked for something the provider can do; only the spelling differs.
4. **Known, the target cannot honor it, and ignoring it does not change what
   the client asked for.** Drop it. A hint the provider would not have acted
   on anyway is not worth a failed request.
5. **Known, the target cannot honor it, and dropping it would change the
   result.** Reject with a 400 that names the field and the provider. A
   silent drop here returns a wrong answer under a 200.

"Known" means the gateway has a rule for the field, not that the field has a
typed struct member. `cache_control` travels in `ExtraFields` and is still a
known field.

Gateway-reserved fields are outside the ladder. Members the gateway itself
sets or interprets, such as `provider` on requests and responses and the
usage options it injects into streams, are never forwarded from the client
and never taken from the provider.

### Scope

The ladder applies to translated paths only: `/v1/*` and the Anthropic
Messages ingress, where the client speaks the gateway's dialect and the
gateway promises to adapt it.

Passthrough routes (`/p/{provider}/...`) are exempt. The client speaks the
provider's dialect and chose the provider by name, so the body reaches the
provider byte for byte. The gateway substitutes credentials and records audit
and usage, and it does not edit the body. The reverse also holds: a translated
endpoint must not be served with passthrough semantics, because every gateway
invariant then has to be re-implemented on the shortcut.

### Where the rules live

Each provider adapter owns its own field decisions, table-driven and tested
per field, as the OpenAI adapter does today. There is no central
field-capability registry; ADR-0004's capability model describes routes and
features, not fields. Revisit if three adapters end up with the same table
shape.

When a field is translated or dropped, the adapter logs it at debug level.
Nothing is added to the response.

### Example ladder for `/v1/chat/completions`

This table records the current decisions so they can be reviewed and updated
as providers change. Add a row when an adapter gains a rule; change a row when
a provider starts accepting a field.

| Field | Target | Rule | Treatment |
|---|---|---|---|
| any unknown member | any | 1 | forwarded untouched |
| `max_tokens` | OpenAI reasoning models | 3 | sent as `max_completion_tokens` |
| `temperature` | OpenAI reasoning models | 4 | dropped |
| `reasoning.effort` | Gemini, DeepSeek | 3 | sent as flat `reasoning_effort` |
| `reasoning.budget_tokens` | Gemini, DeepSeek | 4 | dropped |
| `cache_control` | providers without Anthropic request shapes | 4 | dropped |
| `response_format` of type `text` | Anthropic | 4 | dropped |
| `response_format` of type `json_schema` | Anthropic | 5 | rejected |
| `verbosity` | Anthropic | 5 | rejected |
| `provider` | any | reserved | set by the gateway |

## Consequences

- Clients get one predictable answer per field and provider, and a 400 only
  when honoring the request is impossible.
- Adapters gain an explicit place to record their decisions, and reviewers
  have a rule to check a new mapping against.
- Response types need the same `ExtraFields` treatment requests already have,
  so rule 1 holds in both directions.
- Some existing behavior may move between rungs once reviewed against the
  ladder; each move is a behavior change and gets its own PR and changelog
  entry.

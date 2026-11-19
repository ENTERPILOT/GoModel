# ADR-0017: Gemini API Ingress

## Status

Accepted

## Context

Clients built on the Google Gen AI SDKs speak the Gemini API dialect:
`POST /v1beta/models/{model}:generateContent` and `:streamGenerateContent`,
authenticated with an `x-goog-api-key` header. GoModel had no route for them.
The Gemini provider does not implement passthrough, so `/p/gemini/...` does not
exist either, and a Gemini SDK client could not use the gateway at all.

ADR-0007 solved the same problem for the Anthropic Messages dialect and named a
Gemini ingress as the next dialect to follow its shape.

## Decision

### Translate at the ingress boundary, as ADR-0007 does

The Gemini request is translated once into the canonical `core.ChatRequest` and
runs through the unchanged chat-completions pipeline: workflow resolution, model
authorization, guardrails, rate limits, budgets, failover, usage, and the
request log. The `core.ChatResponse` (or the chat SSE stream) is rendered back
in the Gemini shape just before it is written. A Gemini-dialect request
therefore reaches **any** provider.

A new package, `internal/geminiapi`, owns the dialect and depends only on
`internal/core`. It does not import the `gemini` provider package: the provider
translates chat to Gemini for the upstream, the ingress translates Gemini to
chat for the gateway, and sharing unexported wire types across that boundary
would couple the two directions.

For streaming, observers wrap the inner canonical stream and the Gemini
converter is the outermost layer, the ordering ADR-0007 chose, so usage and the
request log need no Gemini-specific code.

### The model comes from the path

The Gemini API names the model and the method in the last path segment. A
gateway model ID may contain slashes (`openai/gpt-4o`) and colons
(`ollama/llama3:8b`), so the route is a wildcard under `/v1beta/models/` and the
method is whatever follows the last colon. Workflow resolution reads the model
from the path for this dialect; a `model` member in the body is ignored.

Because the path carries the model, usage is recorded under the path template
(`/v1beta/models/{model}:generateContent`), not the request path. Otherwise
every model would be its own endpoint in usage breakdowns and operation filters
could not match the endpoint by equality. The request log keeps the real path.

### Gemini credentials map onto gateway keys

On `/v1beta` routes the gateway key may arrive as `x-goog-api-key` or as the
`?key=` query parameter, after `Authorization` and `x-api-key`. Both are read on
those routes only, so no other route starts accepting a credential in its URL.
The query key is redacted from the access log by the existing URI redaction and
removed from the request URL once the auth middleware has read it.

### Errors

Every failure on a `/v1beta` path, including authentication and unknown routes,
uses the Gemini envelope (`{"error": {"code", "message", "status"}}`), with the
HTTP status mapped to the canonical `google.rpc.Code` name.

### Provider-agnostic translation

The translation does not look at the provider the request resolves to, so a
failover target of another provider type receives a request it can serve.
Members without a portable equivalent follow the ADR-0011 ladder:

- dropped: `topK` (the ADR-0007 decision: OpenAI-family providers reject
  unknown request fields) and `safetySettings` (no other provider has an
  equivalent, so the serving provider's own safety defaults apply; rejecting
  would turn away the many clients that set them on every request);
- rejected with a `400` naming the member, because dropping them would change
  the answer: `cachedContent`, built-in tools (`googleSearch`, `codeExecution`,
  `urlContext`, ...), `fileData`, `executableCode` and `codeExecutionResult`
  parts, `candidateCount` above 1, and `responseModalities` other than text.

### What is not implemented in the first slice

- **Native forwarding to Gemini.** A request that resolves to a Gemini provider
  is translated Gemini → chat → Gemini, so the members above are lost or refused
  there too. A verbatim fast path, like the one `/v1/messages` has for
  Anthropic, needs passthrough support in the Gemini provider and Gemini-native
  usage extraction.
- **The response cache.** It keys on a fixed request path.
- **Other methods:** `countTokens`, `embedContent`, `batchEmbedContents`,
  `batchGenerateContent`, the Files and cached-content APIs, and the Vertex AI
  path layout.
- **`streamGenerateContent` without `alt=sse`** (a streamed JSON array). The
  SDKs always ask for SSE.

## Consequences

### Positive

- Gemini SDK clients work against the gateway by changing the base URL and key,
  and can reach non-Gemini models.
- The managed features apply with no changes to `gateway`, `providers`, `usage`,
  or `requestlog`.

### Negative

- The round trip to a Gemini model is lossy until native forwarding exists.
  Thought summaries are one visible case: the Gemini provider does not surface
  them on the canonical response, so `includeThoughts` returns reasoning from
  other providers but not from Gemini.
- A third chat dispatch function (`dispatchGenerateContent`) repeats the shape of
  the chat and Messages ones. Three dialects now share it; factoring the common
  part is worth doing when a fourth appears or when the three drift.
- A credential in a URL is weaker than one in a header: proxies and load
  balancers in front of the gateway may log it. It is accepted for compatibility
  and documented as the fallback.

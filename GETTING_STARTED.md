# Getting Started

## How configuration is loaded

Configuration loads in a strict three-layer pipeline. Each layer can only add to or override what came before it; nothing is re-read after the pipeline completes.

```
1. Code defaults  (built-in safe values)
        ↓ overlaid by
2. config/config.yaml  (optional, supports ${VAR:-default} expansion)
        ↓ overlaid by
3. Environment variables  (.env file loaded first, then the real process environment)
```

Provider credentials follow the same pipeline but go through an additional resolution step:

```
YAML providers: section
        ↓ env vars applied  (OPENAI_API_KEY, ANTHROPIC_BASE_URL, etc.)
        ↓ filtered          (drop entries with missing or unresolved credentials)
        ↓ resilience merged (per-provider overrides applied on top of global defaults)
→ fully resolved provider, ready to handle requests
```

### Where to put the config file

The loader checks these two paths in order; the first file found wins:

1. `config/config.yaml` (recommended — keeps config separate from the working directory)
2. `config.yaml` (working directory root)

If neither file exists, that is not an error — only code defaults and env vars apply.

---

## Scenarios

### Scenario 1 — env-only, no YAML (simplest deployment)

No `config/config.yaml` exists. All provider credentials come from environment variables. Everything else runs on code defaults.

**.env**

```bash
GOMODEL_MASTER_KEY=super-secret

OPENAI_API_KEY=sk-proj-...
ANTHROPIC_API_KEY=sk-ant-...
# Ollama uses http://localhost:11434/v1 by default — only set this if yours is elsewhere
# OLLAMA_BASE_URL=http://custom-ollama:11434/v1
```

**What happens step by step:**

1. No YAML file is found — provider list starts empty.
2. `OPENAI_API_KEY` and `ANTHROPIC_API_KEY` are discovered and turned into provider entries.
3. Both pass credential filtering; Ollama is also included unconditionally (it needs no API key).
4. All providers receive the built-in default resilience settings.

**Effective resilience for every provider:**

```
retry:
  max_retries:     3
  initial_backoff: 1s
  max_backoff:     30s
  backoff_factor:  2.0
  jitter_factor:   0.1
circuit_breaker:
  failure_threshold: 5   # open after 5 consecutive failures
  success_threshold: 2   # close again after 2 consecutive successes
  timeout:           30s # how long to stay open before probing
```

---

### Scenario 2 — YAML with providers block and per-provider resilience tuning

You want tighter retry limits globally and a more aggressive circuit breaker for a specific noisy provider.

**config/config.yaml**

```yaml
resilience:
  retry:
    max_retries: 2
    initial_backoff: 500ms
    max_backoff: 10s
    backoff_factor: 1.5
    jitter_factor: 0.05
  circuit_breaker:
    failure_threshold: 3
    success_threshold: 1
    timeout: 15s

providers:
  openai:
    type: openai
    api_key: ${OPENAI_API_KEY}
  anthropic:
    type: anthropic
    api_key: ${ANTHROPIC_API_KEY}
    resilience:
      retry:
        max_retries: 5 # Anthropic supports long requests — allow more retries
  ollama:
    type: ollama
    base_url: ${OLLAMA_BASE_URL:-http://localhost:11434/v1}
    resilience:
      circuit_breaker:
        failure_threshold: 10 # local service — tolerate more transient failures
        timeout: 5s
```

**Effective resilience per provider:**

| Provider  | max_retries      | failure_threshold | cb timeout        |
| --------- | ---------------- | ----------------- | ----------------- |
| openai    | 2 (global)       | 3 (global)        | 15s (global)      |
| anthropic | **5** (override) | 3 (global)        | 15s (global)      |
| ollama    | 2 (global)       | **10** (override) | **5s** (override) |

Only fields that are explicitly listed under a provider's `resilience:` block are overridden. Everything else silently inherits from the global section.

---

### Scenario 3 — YAML for resilience only, providers from env

You want resilience settings in version-controlled config but do not want credentials committed to the repository.

**config/config.yaml**

```yaml
resilience:
  retry:
    max_retries: 4
    initial_backoff: 2s
  circuit_breaker:
    failure_threshold: 5
    success_threshold: 2
    timeout: 60s
```

**.env** (not committed)

```bash
OPENAI_API_KEY=sk-proj-...
GROQ_API_KEY=gsk_...
```

**What happens:**

1. YAML sets `max_retries: 4` and `initial_backoff: 2s`. The other retry fields (`max_backoff`, `backoff_factor`, `jitter_factor`) are not listed, so they keep the code defaults.
2. The `providers:` key is absent from the YAML — provider list starts empty.
3. `OPENAI_API_KEY` and `GROQ_API_KEY` are discovered from the environment and turned into provider entries.
4. Both providers inherit the YAML-sourced global resilience config; there are no per-provider overrides.

---

## Environment variable reference

All resilience settings can be overridden at runtime via env vars. Env vars always beat both code defaults and YAML values.

| Variable                            | Type     | Default   | Description                                                                                                                                                                                                                                                 |
| ----------------------------------- | -------- | --------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `RETRY_MAX_RETRIES`                 | int      | `3`       | Maximum retry attempts per request                                                                                                                                                                                                                          |
| `RETRY_INITIAL_BACKOFF`             | duration | `1s`      | First retry wait (e.g. `500ms`, `2s`)                                                                                                                                                                                                                       |
| `RETRY_MAX_BACKOFF`                 | duration | `30s`     | Upper cap on retry wait                                                                                                                                                                                                                                     |
| `RETRY_BACKOFF_FACTOR`              | float    | `2.0`     | Exponential multiplier between retries                                                                                                                                                                                                                      |
| `RETRY_JITTER_FACTOR`               | float    | `0.1`     | Random jitter as a fraction of the backoff                                                                                                                                                                                                                  |
| `CIRCUIT_BREAKER_FAILURE_THRESHOLD` | int      | `5`       | Consecutive failures before opening                                                                                                                                                                                                                         |
| `CIRCUIT_BREAKER_SUCCESS_THRESHOLD` | int      | `2`       | Consecutive successes to close again                                                                                                                                                                                                                        |
| `CIRCUIT_BREAKER_TIMEOUT`           | duration | `30s`     | How long the circuit stays open                                                                                                                                                                                                                             |
| `LOG_FORMAT`                        | string   | _(unset)_ | Auto-detects based on environment: colorized text on a TTY, JSON otherwise. Set to `text` to force human-readable output (no colors if not a TTY), or `json` to force structured JSON even on a TTY (recommended for production, CloudWatch, Datadog, GCP). |
| `LOG_LEVEL`                         | string   | `info`    | Minimum runtime log level. Supported values are `debug`, `info`, `warn`, and `error`. Common aliases such as `dbg`, `inf`, `warning`, and `err` are also accepted.                                                                                          |

Provider credentials:

| Variable              | Provider                                                                       |
| --------------------- | ------------------------------------------------------------------------------ |
| `OPENAI_API_KEY`      | OpenAI                                                                         |
| `OPENAI_BASE_URL`     | OpenAI (custom endpoint)                                                       |
| `ANTHROPIC_API_KEY`   | Anthropic                                                                      |
| `ANTHROPIC_BASE_URL`  | Anthropic (custom endpoint)                                                    |
| `GEMINI_API_KEY`      | Google Gemini                                                                  |
| `GEMINI_BASE_URL`     | Gemini (custom endpoint)                                                       |
| `OPENROUTER_API_KEY`  | OpenRouter (default base URL: `https://openrouter.ai/api/v1`)                  |
| `OPENROUTER_BASE_URL` | OpenRouter (custom endpoint override)                                          |
| `OPENROUTER_SITE_URL` | OpenRouter attribution URL override (default: `https://gomodel.enterpilot.io`) |
| `OPENROUTER_APP_NAME` | OpenRouter attribution title override (default: `GoModel`)                     |
| `XAI_API_KEY`         | xAI / Grok                                                                     |
| `XAI_BASE_URL`        | xAI (custom endpoint)                                                          |
| `GROQ_API_KEY`        | Groq                                                                           |
| `GROQ_BASE_URL`       | Groq (custom endpoint)                                                         |
| `AZURE_API_KEY`       | Azure OpenAI                                                                   |
| `AZURE_BASE_URL`      | Azure OpenAI deployment base URL                                               |
| `AZURE_API_VERSION`   | Azure OpenAI API version override (default: `2024-10-21`)                      |
| `ORACLE_API_KEY`      | Oracle                                                                         |
| `ORACLE_BASE_URL`     | Oracle OpenAI-compatible base URL                                              |
| `OLLAMA_BASE_URL`     | Ollama (default: `http://localhost:11434/v1`)                                  |

See `.env.template` for the full list of all configurable environment variables.

---

## Common gotchas

**Unresolved `${VAR}` placeholders drop the provider.**
If `${OPENAI_API_KEY}` is in your YAML but the env var is not actually set, the literal string `${OPENAI_API_KEY}` ends up as the API key value. The credential filter detects the `${` prefix and removes the provider. Always verify your env vars are exported before starting the process.

**Per-provider resilience can only come from YAML, not from env vars.**
The env var override walk skips maps. `RETRY_MAX_RETRIES` changes the global default for all providers but cannot target a single provider. Per-provider tuning requires a `providers.<name>.resilience:` block in `config.yaml`.

**Env vars override YAML globals.**
Setting `CIRCUIT_BREAKER_TIMEOUT=60s` in the environment overrides whatever `timeout` is written in `config.yaml`, regardless of order or file contents.

**Ollama is always active.**
Ollama requires no API key. Even with no YAML and no `OLLAMA_BASE_URL` set, an Ollama provider is registered pointing at `http://localhost:11434/v1`. If it is unreachable at startup, the gateway still starts and keeps retrying model discovery on the configured refresh interval.

**Azure requires both key and base URL.**
`AZURE_API_KEY` alone is not enough for auto-discovery. Set `AZURE_BASE_URL` to the Azure deployment endpoint as well, otherwise the provider is ignored.

**Oracle requires both key and base URL.**
`ORACLE_API_KEY` alone is not enough for auto-discovery. Set `ORACLE_BASE_URL` to the Oracle OpenAI-compatible endpoint, otherwise the provider is ignored.
If your Oracle endpoint does not return a usable model list, configure `providers.<name>.models` in YAML to seed the router with explicit model IDs.

**Azure ships with a pinned API version by default.**
If you do not set `AZURE_API_VERSION`, the gateway sends `api-version=2024-10-21`. Override it only when you need a different Azure API version.

**OpenRouter gets GoModel attribution headers by default.**
When the `openrouter` provider is used, the gateway adds `HTTP-Referer` and `X-OpenRouter-Title` unless the request already provides them. Override the defaults with `OPENROUTER_SITE_URL` and `OPENROUTER_APP_NAME`.

**Partial YAML fields leave the rest at defaults.**
YAML is unmarshalled onto the struct that was already populated by built-in defaults. Only fields that appear in the file are written. Omitting `max_backoff` from `resilience.retry` leaves it at `30s`; you do not need to repeat defaults you are happy with.

**Two YAML search paths, first wins.**
`config/config.yaml` is checked before `config.yaml` in the working directory. They are not merged — whichever is found first is the only one used.

---

## API Examples

### OpenAI Examples

#### Basic Chat Completion

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "What is the capital of France?"}
    ]
  }'
```

#### Chat Completion with Parameters

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [
      {"role": "system", "content": "You are a helpful assistant."},
      {"role": "user", "content": "Write a haiku about programming."}
    ],
    "temperature": 0.7,
    "max_tokens": 100
  }'
```

#### Chat Completion with Function Calling

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "What is the weather in Warsaw?"}
    ],
    "tools": [
      {
        "type": "function",
        "function": {
          "name": "lookup_weather",
          "description": "Get the weather for a city.",
          "parameters": {
            "type": "object",
            "properties": {
              "city": {"type": "string"}
            },
            "required": ["city"]
          }
        }
      }
    ],
    "tool_choice": {
      "type": "function",
      "function": {"name": "lookup_weather"}
    }
  }'
```

#### Streaming Response

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -N \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "Tell me a short story."}
    ],
    "stream": true
  }'
```

### Anthropic Examples

#### Basic Chat Completion

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-5-sonnet-20241022",
    "messages": [
      {"role": "user", "content": "What is the capital of France?"}
    ]
  }'
```

#### Chat Completion with System Message

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-5-sonnet-20241022",
    "messages": [
      {"role": "system", "content": "You are a creative writing assistant."},
      {"role": "user", "content": "Write a haiku about the ocean."}
    ],
    "temperature": 0.8,
    "max_tokens": 200
  }'
```

#### Streaming Response

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -N \
  -d '{
    "model": "claude-3-5-haiku-20241022",
    "messages": [
      {"role": "user", "content": "Explain quantum computing in simple terms."}
    ],
    "stream": true
  }'
```

#### Using Claude Opus (Most Capable Model)

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-opus-20240229",
    "messages": [
      {"role": "user", "content": "Analyze the pros and cons of renewable energy."}
    ],
    "max_tokens": 1000
  }'
```

### Google Gemini Examples

#### Basic Chat Completion

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-3-flash-preview",
    "messages": [
      {"role": "user", "content": "What is the capital of France?"}
    ]
  }'
```

#### Chat Completion with Parameters

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-1.5-pro",
    "messages": [
      {"role": "system", "content": "You are a knowledgeable science educator."},
      {"role": "user", "content": "Explain photosynthesis in simple terms."}
    ],
    "temperature": 0.7,
    "max_tokens": 500
  }'
```

#### Streaming Response

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -N \
  -d '{
    "model": "gemini-2.0-flash",
    "messages": [
      {"role": "user", "content": "Write a short poem about AI."}
    ],
    "stream": true
  }'
```

### xAI Examples

#### Basic Responses API Request

```bash
curl http://localhost:8080/v1/responses \
  -H "Content-Type: application/json" \
  -d '{
    "model": "grok-4-1-fast-non-reasoning",
    "input": "What is the capital of France?"
  }'
```

#### Responses API Request with Instructions

```bash
curl http://localhost:8080/v1/responses \
  -H "Content-Type: application/json" \
  -d '{
    "model": "grok-4-1-fast-non-reasoning",
    "input": "Write a haiku about programming.",
    "instructions": "You are a creative AI assistant who specializes in writing poetry.",
    "temperature": 0.8,
    "max_output_tokens": 200
  }'
```

#### Streaming Responses

```bash
curl http://localhost:8080/v1/responses \
  -H "Content-Type: application/json" \
  -N \
  -d '{
    "model": "grok-4-1-fast-non-reasoning",
    "input": "Tell me a short story about AI.",
    "stream": true
  }'
```

### Embeddings

#### Basic Embedding

```bash
curl http://localhost:8080/v1/embeddings \
  -H "Content-Type: application/json" \
  -d '{
    "model": "text-embedding-3-small",
    "input": "The quick brown fox jumps over the lazy dog."
  }'
```

#### Batch Embedding (multiple inputs)

```bash
curl http://localhost:8080/v1/embeddings \
  -H "Content-Type: application/json" \
  -d '{
    "model": "text-embedding-3-small",
    "input": ["First sentence", "Second sentence", "Third sentence"]
  }'
```

#### With Custom Dimensions

```bash
curl http://localhost:8080/v1/embeddings \
  -H "Content-Type: application/json" \
  -d '{
    "model": "text-embedding-3-large",
    "input": "Hello world",
    "dimensions": 512
  }'
```

Supported by: OpenAI, Gemini, Groq, xAI, Ollama. Anthropic does not support embeddings natively.

### List Available Models

```bash
curl http://localhost:8080/v1/models
```

Example response:

```json
{
  "object": "list",
  "data": [
    {
      "id": "gpt-4o",
      "object": "model",
      "owned_by": "openai",
      "created": 1234567890
    },
    {
      "id": "claude-3-5-sonnet-20241022",
      "object": "model",
      "owned_by": "anthropic",
      "created": 1234567890
    }
  ]
}
```

### Health Check

```bash
curl http://localhost:8080/health
```

---

## Client library examples

### Python

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:8080/v1",
    api_key="not-needed"  # API key is configured on the server side
)

# Use OpenAI models
response = client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "Hello!"}]
)
print(response.choices[0].message.content)

# Or use Anthropic models with the same interface
response = client.chat.completions.create(
    model="claude-3-5-sonnet-20241022",
    messages=[{"role": "user", "content": "Hello!"}]
)
print(response.choices[0].message.content)

# Streaming works too
stream = client.chat.completions.create(
    model="claude-3-5-haiku-20241022",
    messages=[{"role": "user", "content": "Tell me a story"}],
    stream=True
)
for chunk in stream:
    if chunk.choices[0].delta.content:
        print(chunk.choices[0].delta.content, end="")

# Embeddings
embedding = client.embeddings.create(
    model="text-embedding-3-small",
    input="Hello world"
)
print(embedding.data[0].embedding[:5])  # first 5 dimensions
```

### Node.js

```javascript
import OpenAI from "openai";

const client = new OpenAI({
  baseURL: "http://localhost:8080/v1",
  apiKey: "not-needed",
});

// Use any supported model — routing is automatic
const response = await client.chat.completions.create({
  model: "gemini-2.0-flash",
  messages: [{ role: "user", content: "Hello!" }],
});
console.log(response.choices[0].message.content);

// Streaming
const stream = await client.chat.completions.create({
  model: "claude-3-5-haiku-20241022",
  messages: [{ role: "user", content: "Tell me a story" }],
  stream: true,
});
for await (const chunk of stream) {
  if (chunk.choices[0]?.delta?.content) {
    process.stdout.write(chunk.choices[0].delta.content);
  }
}

// Embeddings
const embedding = await client.embeddings.create({
  model: "text-embedding-3-small",
  input: "Hello world",
});
console.log(embedding.data[0].embedding.slice(0, 5)); // first 5 dimensions
```

---

## Available models

### OpenAI

- `gpt-4o` - Most capable GPT-4 model
- `gpt-4o-mini` - Fast and efficient GPT-4 model
- `gpt-4-turbo` - Previous generation GPT-4 Turbo
- `gpt-3.5-turbo` - Fast and cost-effective
- `o1-preview` - Advanced reasoning model (preview)
- `o1-mini` - Faster reasoning model

### Anthropic

- `claude-3-5-sonnet-20241022` - Latest Sonnet (best balance of speed and capability)
- `claude-3-5-haiku-20241022` - Latest Haiku (fastest, most cost-effective)
- `claude-3-opus-20240229` - Most capable Claude model

### Google Gemini

- `gemini-2.0-flash` - Latest Flash model (fast and efficient)
- `gemini-1.5-pro` - Most capable Gemini model (large context window)
- `gemini-1.5-flash` - Previous generation Flash model

### xAI

- `grok-4-1-fast-non-reasoning` - Most capable Grok model

### Groq

- Models are fetched dynamically from the Groq API at startup

### Ollama

- Models are fetched dynamically from your local Ollama instance at startup

---

## Tips

1. **Model routing**: The gateway automatically routes requests to the correct provider based on the model name — no configuration needed. Just use any model name from the list above.
2. **API compatibility**: The gateway exposes an OpenAI-compatible API. Existing OpenAI client libraries work unchanged for all providers.
3. **Streaming**: All providers support streaming. SSE chunks are flushed incrementally, and streaming responses terminate with `data: [DONE]`.
4. **System messages**: Anthropic's system message format is handled automatically. Gemini uses Google's OpenAI-compatible endpoint, which also handles system messages natively.
5. **Max tokens**: Anthropic requires `max_tokens` to be set. If not provided, the gateway defaults to 4096. OpenAI and Gemini treat it as optional.
6. **Responses API**: The `/v1/responses` endpoint provides a unified interface across all providers. Providers that do not natively support the Responses API convert requests internally.
7. **Embeddings**: The `/v1/embeddings` endpoint is supported by OpenAI, Gemini, Groq, xAI, and Ollama. Anthropic does not offer embeddings natively.

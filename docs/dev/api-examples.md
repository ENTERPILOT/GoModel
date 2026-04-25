# API Examples

Reference curl examples for the OpenAI-compatible endpoints exposed by GoModel.
The user-facing quickstart lives at
[`docs/getting-started/quickstart.mdx`](../getting-started/quickstart.mdx); this
file is a longer collection useful when manually exercising providers.

All examples assume:

- GoModel is reachable at `http://localhost:8080`
- The relevant provider credential is configured server-side
- If `GOMODEL_MASTER_KEY` is set, add `-H "Authorization: Bearer $GOMODEL_MASTER_KEY"`
  to every request

Streaming responses are SSE: chunks are flushed incrementally and the stream
terminates with `data: [DONE]`.

## OpenAI

### Basic Chat Completion

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

### Chat Completion with Parameters

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

### Chat Completion with Function Calling

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

### Streaming Response

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

## Anthropic

### Basic Chat Completion

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

### Chat Completion with System Message

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

### Streaming Response

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

### Using Claude Opus

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

## Google Gemini

### Basic Chat Completion

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

### Chat Completion with Parameters

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

### Streaming Response

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

## xAI (Responses API)

### Basic Responses Request

```bash
curl http://localhost:8080/v1/responses \
  -H "Content-Type: application/json" \
  -d '{
    "model": "grok-4-1-fast-non-reasoning",
    "input": "What is the capital of France?"
  }'
```

### Responses Request with Instructions

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

### Streaming Responses

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

## Embeddings

Supported by: OpenAI, Gemini, Groq, Z.ai, xAI, Ollama. Anthropic does not
support embeddings natively.

### Basic Embedding

```bash
curl http://localhost:8080/v1/embeddings \
  -H "Content-Type: application/json" \
  -d '{
    "model": "text-embedding-3-small",
    "input": "The quick brown fox jumps over the lazy dog."
  }'
```

### Batch Embedding

```bash
curl http://localhost:8080/v1/embeddings \
  -H "Content-Type: application/json" \
  -d '{
    "model": "text-embedding-3-small",
    "input": ["First sentence", "Second sentence", "Third sentence"]
  }'
```

### Custom Dimensions

```bash
curl http://localhost:8080/v1/embeddings \
  -H "Content-Type: application/json" \
  -d '{
    "model": "text-embedding-3-large",
    "input": "Hello world",
    "dimensions": 512
  }'
```

## List Available Models

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

## Health Check

```bash
curl http://localhost:8080/health
```

## Client Library Examples

GoModel exposes an OpenAI-compatible API, so the official OpenAI SDKs work
unchanged — just point them at the gateway. The model name selects the
upstream provider; routing is automatic.

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

// Use any supported model - routing is automatic
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

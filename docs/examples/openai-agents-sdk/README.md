# OpenAI Agents SDK examples

These examples point the OpenAI Agents SDK at a local GoModel instance.

Start GoModel first:

```bash
docker run --rm -p 8080:8080 \
  -e GOMODEL_MASTER_KEY="change-me" \
  -e OPENAI_API_KEY="sk-..." \
  enterpilot/gomodel
```

Then run one of the examples:

```bash
export OPENAI_BASE_URL=http://localhost:8080/v1
export GOMODEL_MASTER_KEY=change-me
export OPENAI_MODEL=gpt-5-mini

python3 python_basic.py
python3 python_streaming_tool.py
node javascript_basic.mjs
```

To probe an Anthropic model through GoModel's OpenAI-compatible Responses API:

```bash
export OPENAI_BASE_URL=http://localhost:8080/v1
export GOMODEL_MASTER_KEY=change-me
export OPENAI_MODEL=anthropic/claude-sonnet-4-20250514

python3 anthropic_responses_probe.py
python3 anthropic_agents_probe.py
```

`anthropic_agents_probe.py` configures the Python SDK's `MultiProvider` with
model ID pass-through so namespaced GoModel IDs such as `anthropic/...` reach
the gateway unchanged.

Install the SDK dependencies in your own environment:

```bash
pip install openai-agents openai
npm install @openai/agents openai
```

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

Install the SDK dependencies in your own environment:

```bash
pip install openai-agents openai
npm install @openai/agents openai
```

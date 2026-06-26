# GoModel quality (QA) suite

A curated corpus of ~50 complex requests that exercises every client-facing
dialect and modality of the gateway against **real providers**
(OpenAI / Anthropic / Gemini), then **registers** and **rates** each one.

It answers a different question than the latency benchmark next door
(`docs/2026-06-25_aws_gateway_benchmark/`): not *how fast/cheap* the gateway is,
but *does it correctly accept, translate, and normalize real-world requests* —
the Postel's-law contract.

For every case the suite records:

- the **request as sent** (after model-role and variable resolution);
- the **response** received (status, headers, body, or assembled SSE text);
- **how the gateway recorded/normalized it** — pulled from the audit log:
  the inbound request body it captured, the normalized response body it
  returned, the resolved provider/model, and token usage;

and rates it `PASS` / `FAIL` / `ERROR` / `SKIP`, plus a 0–100 **quality score**
for soft modality checks (did the vision model name the colour, did STT recover
the spoken words).

## What it covers

| Dialect / endpoint | Providers | Modalities exercised |
|---|---|---|
| `/v1/chat/completions` | OpenAI, Anthropic, Gemini | text, multi-turn, streaming, vision, tools, reasoning, structured output, field preservation |
| `/v1/responses` | OpenAI, Anthropic, Gemini | text, multimodal input, streaming, tools, structured output, reasoning, conversation linkage |
| `/v1/messages` (+ `/count_tokens`) | Anthropic | native shape, system prompt, streaming SSE, vision blocks, tool_use, extended thinking, default `max_tokens` injection |
| `/v1/conversations` | OpenAI | create → get → use-in-Responses → update → delete (stateful) |
| `/v1/audio/speech`, `/v1/audio/transcriptions` | OpenAI | TTS, and a TTS→STT round-trip that recovers the spoken words |
| `/v1/embeddings` | OpenAI | single + batch |
| error normalization | OpenAI, Anthropic | unknown model, unsupported `input_audio`, malformed JSON |

## How "field preservation" is verified (and its honest limit)

GoModel's audit log captures the **inbound** client request body and the
**normalized** response body it returns — *not* the upstream provider-translated
request. So the suite verifies translation two ways:

1. **Behaviorally** — e.g. the reasoning case sends `max_tokens` to a model that
   rejects it upstream; a `200` proves the gateway mapped it to
   `max_completion_tokens` and dropped `temperature`. The audio-rejection case
   proves an unsupported modality fails cleanly (4xx) rather than crashing.
2. **From the audit record** — extra/unknown request fields (`x_qa_marker`,
   `metadata`) are asserted present in the captured inbound body, and
   provider-specific response extras (`system_fingerprint`, `service_tier`,
   `stop_reason`, `usage`) are asserted preserved in the normalized response.

Audit cross-checks are **soft** by default: if audit bodies are off or the entry
hasn't flushed, those checks are skipped with a note, never a false failure.

## Prerequisites

Run the gateway with audit logging **and bodies** enabled so the preservation
checks have data:

```bash
LOGGING_ENABLED=true \
LOGGING_LOG_BODIES=true \
LOGGING_LOG_HEADERS=true \
LOGGING_LOG_AUDIO_BODIES=true \
LOGGING_FLUSH_INTERVAL=2 \
./gomodel        # or: go run ./cmd/gomodel
```

Provider keys come from the gateway's environment (`OPENAI_API_KEY`,
`ANTHROPIC_API_KEY`, `GEMINI_API_KEY`). The harness authenticates to the gateway
with `GOMODEL_MASTER_KEY` (read from the env or the repo `.env`).

> This calls real providers and spends real money — modest (a few cents) for one
> full run, since payloads are tiny and `max_tokens` is capped on every case.

## Run it

```bash
cd docs/2026-06-25_aws_gateway_benchmark/qa
python3 run_qa.py                      # full corpus against http://localhost:8080
python3 run_qa.py --only chat          # filter by id/group/provider substring
python3 run_qa.py --only openai
python3 run_qa.py --no-audit           # skip audit cross-checks (faster, fewer assertions)
python3 run_qa.py --list               # list matching cases, don't run
python3 run_qa.py --gateway http://host:8080
```

Stdlib only — no `pip install`. Exit code is non-zero if any case failed or
errored. Results land in `output/<run_id>/`:

- `results.json` — full per-case record (request sent, response, audit view, every assertion)
- `report.md` — readable table + a drill-down of failed/errored cases

## Adapt to your account

The spec never hardcodes a model id. Cases reference logical roles
(`@openai.chat`, `@anthropic.thinking`, `@gemini.vision`); edit `models.json` to
map them to models your keys can reach. A role with no mapping makes its cases
`SKIP`, never fail. Image inputs (`@image.red` / `@imageb64.red`) are generated
solid-colour PNGs — no binary assets in the repo.

## Layout

```
run_qa.py        orchestrator + assertion evaluation + CLI
models.json      logical model roles -> concrete model ids (edit this)
spec/            declarative cases, one JSON file per endpoint group
qalib/           stdlib helpers: config, paths, assertions, client, report
output/          run artifacts (gitignored)
```

## Case schema (quick reference)

```jsonc
{
  "id": "chat.openai.multiturn",          // unique
  "title": "...", "provider": "openai",
  "modality": ["text"],                    // labels for reporting
  "request": {
    "method": "POST",                      // default POST
    "path": "/v1/chat/completions",        // may contain ${captured_var}
    "headers": {"X-QA-Marker": "keep"},
    "stream": false,
    "body": { "model": "@openai.chat", "...": "..." },
    "raw_body": "…",                       // send verbatim (malformed-JSON tests)
    "produce": "tts_then_stt",             // composite: TTS then transcribe its output
    "tts": {...}, "stt": {...}             // inputs for produce=tts_then_stt
  },
  "capture": { "conversation_id": "$.id" },// save response values for later ${vars}
  "expect": {
    "status": 200,                         // int or list
    "headers":  [ {"name": "X-Request-Id", "present": true} ],
    "body":     [ {"field": "content_type", "contains": "audio/"},
                  {"field": "bytes", "gte": 2000},
                  {"field": "text",  "not_empty": true} ],
    "response": [ {"path": "$.choices[0].message.content", "not_empty": true} ],
    "stream":   { "min_events": 2, "terminal": "[DONE]",
                  "event_types": ["message_start"], "text": [{"not_empty": true}] },
    "audit":    [ {"path": "$.provider", "equals": "openai"},
                  {"path": "$.data.request_body.x_qa_marker", "equals": "keep"} ],
    "quality":  [ {"target": "response:$.output[0].content[0].text",
                   "contains_any": ["paris"]} ]   // soft; feeds the score
  }
}
```

**Operators** (one per assertion): `present` · `absent` · `equals` ·
`not_equals` · `not_empty` · `contains` · `not_contains` · `contains_any` ·
`contains_all` · `regex` · `gt` · `gte` · `lt` · `lte` · `type` · `length_gte` ·
`one_of`. Add `"hard": false` to make a failure a soft signal instead of failing
the case (audit and quality checks are soft by default).

**Quality targets:** `stream` · `body.text` · `response:$.path` · `audit:$.path`.

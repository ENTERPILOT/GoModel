# Gateway translation-fidelity analysis

How faithfully does each AI gateway translate a request? This harness sends the
**same** client request through **GoModel, LiteLLM, Portkey, and Bifrost**, all
pointed at the **same recording mock provider**, and captures — per case, per
gateway — four artifacts:

| artifact | meaning |
|---|---|
| `client_request` | what we sent to the gateway (the **pure** request) |
| `sent_body` | the body after per-gateway rewrites (e.g. Bifrost's `openai/` model prefix) |
| `upstream` | the request the gateway actually sent to the provider (the **translated** request) + the canned (**pure**) response the mock returned |
| `client_response` | what the gateway returned to us (the **translated** response) |

Then an AI analyzes each case across gateways: what each one added, dropped,
renamed, or reshaped — request *and* response — and which is most faithful.

A recording mock (not real providers) is the only way to observe the translated
*upstream* request: real providers don't echo what the gateway sent them.

## Why a mock, and what "pure" means

- **Pure request** = the original client body. **Translated request** = what the
  gateway emitted upstream (captured by the mock).
- **Pure response** = the deterministic provider-shaped body the mock returned
  (enriched with `system_fingerprint`, `service_tier`, and a non-standard
  `x_provider_note` so we can see which gateways preserve provider extras).
  **Translated response** = what the gateway returned to the client.
- The comparison axis is **gateway vs gateway** — every case uses the same model
  (`gpt-4o-mini`) routed to the mock, so differences are the gateway's doing, not
  the provider's.

## Pieces

```
docker-compose.yml   mock (MOCK_RECORD=1) + all 4 gateways, reusing ../remote configs
corpus.json          12 gateway-agnostic cases across chat/responses/messages, stream + not
capture.py           resets the mock, sends each case through each gateway, records 4 artifacts
analyze.py           builds per-case AI-analysis prompts from the captures (one bundle per case)
output/              captures.json + the AI comparison report (gitignored)
```

The recording mock lives in `../remote/bench-tools/mock/main.go` (recording is
gated behind `MOCK_RECORD=1`, so the latency benchmark stays byte-identical).

## Run it

```bash
# 0. build the GoModel image once (native arch):
docker build -t gomodel-bench:local ../../..

# 1. bring up the recording mock + all four gateways:
cd docs/2026-06-25_aws_gateway_benchmark/translation
docker compose --profile all up -d --build

# 2. capture translations (resets the mock before each call):
python3 capture.py            # -> output/captures.json

# 3. tear down:
docker compose --profile all down
```

No real provider keys or spend — every gateway talks to the local mock.

## Per-gateway addressing (handled by capture.py)

| gateway | port | model | messages path | extra headers |
|---|--|---|---|---|
| GoModel | 8080 | `gpt-4o-mini` | `/v1/messages` | — |
| LiteLLM | 4000 | `gpt-4o-mini` | `/v1/messages` | — |
| Portkey | 8787 | `gpt-4o-mini` | `/v1/messages` | `x-portkey-provider`, `x-portkey-custom-host` |
| Bifrost | 8089 | `openai/gpt-4o-mini` | `/anthropic/v1/messages` | — |

Dialects a gateway doesn't serve are not skipped — the non-200 (and empty
upstream log) is recorded, because that asymmetry is itself a finding.

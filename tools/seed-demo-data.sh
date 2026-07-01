#!/usr/bin/env bash
set -euo pipefail

db_path="${SQLITE_PATH:-data/gomodel.db}"
days="${DEMO_DAYS:-90}"
end_date="${DEMO_END_DATE:-}"
avg_requests="${DEMO_AVG_REQUESTS_PER_DAY:-850}"
max_requests="${DEMO_MAX_REQUESTS_PER_DAY:-1600}"
exact_cache_pct="${DEMO_EXACT_CACHE_PCT:-12}"
semantic_cache_pct="${DEMO_SEMANTIC_CACHE_PCT:-7}"
prompt_cache_pct="${DEMO_PROMPT_CACHE_PCT:-28}"
prefix="${DEMO_SEED_PREFIX:-demo-generated}"

usage() {
  cat <<EOF
Usage: [env...] tools/seed-demo-data.sh

Environment:
  SQLITE_PATH                     SQLite DB path (default: data/gomodel.db)
  DEMO_DAYS                       Rolling day count (default: 90)
  DEMO_END_DATE                   End date YYYY-MM-DD (default: today UTC)
  DEMO_AVG_REQUESTS_PER_DAY       Average daily request count (default: 850)
  DEMO_MAX_REQUESTS_PER_DAY       Upper slot cap per day (default: 1600)
  DEMO_EXACT_CACHE_PCT            Local exact cache hit percentage (default: 12)
  DEMO_SEMANTIC_CACHE_PCT         Local semantic cache hit percentage (default: 7)
  DEMO_PROMPT_CACHE_PCT           Provider prompt-cache percentage (default: 28)
  DEMO_SEED_PREFIX                Row ID/budget source prefix; reruns replace this prefix only (default: demo-generated)
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

require_int() {
  local name="$1"
  local value="$2"
  if ! [[ "$value" =~ ^[0-9]+$ ]]; then
    echo "$name must be a non-negative integer, got: $value" >&2
    exit 2
  fi
}

require_int DEMO_DAYS "$days"
require_int DEMO_AVG_REQUESTS_PER_DAY "$avg_requests"
require_int DEMO_MAX_REQUESTS_PER_DAY "$max_requests"
require_int DEMO_EXACT_CACHE_PCT "$exact_cache_pct"
require_int DEMO_SEMANTIC_CACHE_PCT "$semantic_cache_pct"
require_int DEMO_PROMPT_CACHE_PCT "$prompt_cache_pct"

if (( days < 1 )); then
  echo "DEMO_DAYS must be at least 1" >&2
  exit 2
fi
if (( max_requests < avg_requests )); then
  echo "DEMO_MAX_REQUESTS_PER_DAY must be >= DEMO_AVG_REQUESTS_PER_DAY" >&2
  exit 2
fi
if (( exact_cache_pct + semantic_cache_pct > 65 )); then
  echo "Exact + semantic cache percentages should stay realistic and <= 65" >&2
  exit 2
fi
if (( prompt_cache_pct > 85 )); then
  echo "DEMO_PROMPT_CACHE_PCT must be <= 85" >&2
  exit 2
fi
if [[ -n "$end_date" && ! "$end_date" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]; then
  echo "DEMO_END_DATE must use YYYY-MM-DD, got: $end_date" >&2
  exit 2
fi
if [[ ! "$prefix" =~ ^[A-Za-z0-9_.-]+$ ]]; then
  echo "DEMO_SEED_PREFIX may only contain letters, numbers, dot, underscore, and dash" >&2
  exit 2
fi

command -v sqlite3 >/dev/null 2>&1 || {
  echo "sqlite3 is required" >&2
  exit 127
}

mkdir -p "$(dirname "$db_path")"

sqlite3 "$db_path" "PRAGMA journal_mode = WAL;" >/dev/null

sqlite3 "$db_path" <<SQL
.bail on
.timeout 10000
PRAGMA synchronous = NORMAL;

CREATE TABLE IF NOT EXISTS usage (
  id TEXT PRIMARY KEY,
  request_id TEXT NOT NULL,
  provider_id TEXT NOT NULL,
  timestamp DATETIME NOT NULL,
  model TEXT NOT NULL,
  provider TEXT NOT NULL,
  provider_name TEXT,
  endpoint TEXT NOT NULL,
  user_path TEXT,
  cache_type TEXT,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  total_tokens INTEGER NOT NULL DEFAULT 0,
  raw_data JSON,
  input_cost REAL,
  output_cost REAL,
  total_cost REAL,
  cost_source TEXT DEFAULT '',
  costs_calculation_caveat TEXT DEFAULT ''
);

CREATE TABLE IF NOT EXISTS audit_logs (
  id TEXT PRIMARY KEY,
  timestamp DATETIME NOT NULL,
  duration_ns INTEGER DEFAULT 0,
  requested_model TEXT,
  resolved_model TEXT,
  provider TEXT,
  provider_name TEXT,
  alias_used INTEGER DEFAULT 0,
  workflow_version_id TEXT,
  cache_type TEXT,
  status_code INTEGER DEFAULT 0,
  request_id TEXT,
  auth_key_id TEXT,
  auth_method TEXT,
  client_ip TEXT,
  method TEXT,
  path TEXT,
  user_path TEXT,
  stream INTEGER DEFAULT 0,
  error_type TEXT,
  data JSON
);

CREATE TABLE IF NOT EXISTS budgets (
  user_path TEXT NOT NULL,
  period_seconds INTEGER NOT NULL,
  amount REAL NOT NULL,
  source TEXT NOT NULL DEFAULT '',
  last_reset_at INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (user_path, period_seconds)
);

CREATE TABLE IF NOT EXISTS budget_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_usage_timestamp ON usage(timestamp);
CREATE INDEX IF NOT EXISTS idx_usage_request_id ON usage(request_id);
CREATE INDEX IF NOT EXISTS idx_usage_provider ON usage(provider);
CREATE INDEX IF NOT EXISTS idx_usage_provider_name ON usage(provider_name);
CREATE INDEX IF NOT EXISTS idx_usage_user_path ON usage(user_path);
CREATE INDEX IF NOT EXISTS idx_usage_cache_type ON usage(cache_type);
CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_logs(timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_request_id ON audit_logs(request_id);
CREATE INDEX IF NOT EXISTS idx_audit_path ON audit_logs(path);
CREATE INDEX IF NOT EXISTS idx_audit_user_path ON audit_logs(user_path);
CREATE INDEX IF NOT EXISTS idx_audit_cache_type ON audit_logs(cache_type);
CREATE INDEX IF NOT EXISTS idx_budgets_user_path ON budgets(user_path);
CREATE INDEX IF NOT EXISTS idx_budgets_period_seconds ON budgets(period_seconds);

BEGIN IMMEDIATE;

DELETE FROM audit_logs WHERE id GLOB '${prefix}-*';
DELETE FROM usage WHERE id GLOB '${prefix}-*';
DELETE FROM budgets WHERE source = '${prefix}';

DROP TABLE IF EXISTS temp.demo_days;
CREATE TEMP TABLE demo_days AS
WITH RECURSIVE days(day_idx, day) AS (
  SELECT 0, date(CASE WHEN '${end_date}' = '' THEN 'now' ELSE '${end_date}' END, '-' || (${days} - 1) || ' days')
  UNION ALL
  SELECT day_idx + 1, date(day, '+1 day') FROM days WHERE day_idx < ${days} - 1
),
daily_random AS (
  SELECT
    day_idx,
    day,
    CASE WHEN strftime('%w', day) IN ('0', '6') THEN 0.68 ELSE 1.0 END AS weekday_factor,
    0.84 + (day_idx * 0.32 / max(1, ${days} - 1)) AS trend_factor,
    0.90 + ((day_idx % 14) * 0.018) AS seasonal_factor,
    0.82 + ((abs(random()) % 3900) / 10000.0) AS noise_factor
  FROM days
)
SELECT
  day_idx,
  day,
  CAST(max(25, min(${max_requests}, round(${avg_requests} * weekday_factor * trend_factor * seasonal_factor * noise_factor))) AS INTEGER) AS request_count
FROM daily_random;

DROP TABLE IF EXISTS temp.demo_slots;
CREATE TEMP TABLE demo_slots(slot_idx INTEGER PRIMARY KEY);
WITH RECURSIVE slots(slot_idx) AS (
  SELECT 0
  UNION ALL
  SELECT slot_idx + 1 FROM slots WHERE slot_idx < ${max_requests} - 1
)
INSERT INTO demo_slots SELECT slot_idx FROM slots;

DROP TABLE IF EXISTS temp.demo_paths;
CREATE TEMP TABLE demo_paths(min_bucket INTEGER, max_bucket INTEGER, user_path TEXT);
INSERT INTO demo_paths VALUES
  (0, 1100, '/agents/team1'),
  (1100, 1800, '/agents/team1/research'),
  (1800, 2850, '/agents/team2'),
  (2850, 3400, '/agents/team2/ops'),
  (3400, 4600, '/engineering/ai/mike'),
  (4600, 5750, '/engineering/ai/mike/evals'),
  (5750, 7050, '/sales/john'),
  (7050, 7650, '/sales/john/prospects'),
  (7650, 9100, '/engineering/ai/bot'),
  (9100, 10000, '/engineering/ai/bot/batch');

DROP TABLE IF EXISTS temp.demo_templates;
CREATE TEMP TABLE demo_templates(
  min_bucket INTEGER,
  max_bucket INTEGER,
  label TEXT,
  endpoint TEXT,
  provider TEXT,
  provider_name TEXT,
  model TEXT,
  input_min INTEGER,
  input_span INTEGER,
  output_min INTEGER,
  output_span INTEGER,
  input_price REAL,
  output_price REAL,
  local_cache_eligible INTEGER,
  prompt_cache_eligible INTEGER
);
INSERT INTO demo_templates VALUES
  (0,    1700, 'chat-openai',      '/v1/chat/completions',    'openai',    'openai',    'gpt-5-nano-2025-08-07',      800,  6500,  120, 1800, 0.050, 0.400, 1, 1),
  (1700, 3150, 'chat-groq',        '/v1/chat/completions',    'groq',      'groq',      'llama-3.1-8b-instant',       600,  4800,   90, 1300, 0.030, 0.220, 1, 1),
  (3150, 4450, 'chat-gemini',      '/v1/chat/completions',    'gemini',    'gemini',    'gemini-2.5-flash-lite',      900,  7200,  100, 1600, 0.040, 0.300, 1, 1),
  (4450, 5650, 'chat-bailian',     '/v1/chat/completions',    'bailian',   'bailian',   'qwen-flash',                 700,  5600,  100, 1500, 0.035, 0.260, 1, 1),
  (5650, 6850, 'responses',        '/v1/responses',           'bailian',   'bailian',   'qwen-flash',                1200,  8200,  180, 2100, 0.035, 0.260, 1, 1),
  (6850, 7850, 'messages',         '/v1/messages',            'anthropic', 'anthropic', 'claude-haiku-4-5-20251001', 1000,  9000,  150, 2200, 0.250, 1.250, 1, 1),
  (7850, 8550, 'embeddings',       '/v1/embeddings',          'openai',    'openai',    'text-embedding-3-small',     300,  2400,    0,    1, 0.020, 0.000, 0, 0),
  (8550, 9250, 'stt',              '/v1/audio/transcriptions','openai',    'openai',    'gpt-4o-transcribe',         600,  5200,   60,  500, 2.500, 0.000, 0, 0),
  (9250, 10000,'tts',              '/v1/audio/speech',        'openai',    'openai',    'tts-1',                     120,  1200,    0,    1, 2.000, 0.000, 0, 0);

DROP TABLE IF EXISTS temp.demo_random;
CREATE TEMP TABLE demo_random AS
SELECT
  d.day_idx,
  d.day,
  s.slot_idx,
  abs(random()) % 10000 AS path_bucket,
  abs(random()) % 10000 AS template_bucket,
  abs(random()) % 10000 AS cache_bucket,
  abs(random()) % 10000 AS prompt_bucket,
  abs(random()) % 86400 AS second_of_day,
  abs(random()) AS token_noise
FROM demo_days d
JOIN demo_slots s ON s.slot_idx < d.request_count;

DROP TABLE IF EXISTS temp.demo_generated;
CREATE TEMP TABLE demo_generated AS
WITH chosen AS (
  SELECT
    b.*,
    p.user_path,
    t.label,
    t.endpoint,
    t.provider,
    t.provider_name,
    t.model,
    t.input_min,
    t.input_span,
    t.output_min,
    t.output_span,
    t.input_price,
    t.output_price,
    t.local_cache_eligible,
    t.prompt_cache_eligible
  FROM demo_random b
  JOIN demo_paths p ON b.path_bucket >= p.min_bucket AND b.path_bucket < p.max_bucket
  JOIN demo_templates t ON b.template_bucket >= t.min_bucket AND b.template_bucket < t.max_bucket
),
tokens AS (
  SELECT
    *,
    input_min + (token_noise % input_span) AS input_tokens,
    output_min + ((token_noise / 97) % output_span) AS output_tokens
  FROM chosen
),
cache_decisions AS (
  SELECT
    *,
    CASE
      WHEN local_cache_eligible = 1 AND cache_bucket < (${exact_cache_pct} * 100) THEN 'exact'
      WHEN local_cache_eligible = 1 AND cache_bucket < ((${exact_cache_pct} + ${semantic_cache_pct}) * 100) THEN 'semantic'
      ELSE NULL
    END AS cache_type,
    CASE
      WHEN prompt_cache_eligible = 1
        AND NOT (local_cache_eligible = 1 AND cache_bucket < ((${exact_cache_pct} + ${semantic_cache_pct}) * 100))
        AND prompt_bucket < (${prompt_cache_pct} * 100)
      THEN 1
      ELSE 0
    END AS prompt_cache_hit
  FROM tokens
),
prompt_parts AS (
  SELECT
    *,
    CASE
      WHEN prompt_cache_hit = 1 THEN CAST(input_tokens * (35 + (prompt_bucket % 46)) / 100 AS INTEGER)
      ELSE 0
    END AS prompt_cached_tokens,
    CASE
      WHEN prompt_cache_hit = 1 AND provider = 'anthropic' THEN CAST(input_tokens * (8 + (prompt_bucket % 13)) / 100 AS INTEGER)
      ELSE 0
    END AS prompt_cache_write_tokens
  FROM cache_decisions
)
SELECT
  *,
  input_tokens + output_tokens AS total_tokens,
  strftime('%Y-%m-%dT%H:%M:%fZ', day || ' 00:00:00', '+' || second_of_day || ' seconds') AS timestamp,
  '${prefix}-usage-' || day_idx || '-' || slot_idx AS usage_id,
  '${prefix}-audit-' || day_idx || '-' || slot_idx AS audit_id,
  '${prefix}-req-' || day_idx || '-' || slot_idx AS request_id,
  '${prefix}-provider-' || day_idx || '-' || slot_idx AS provider_id
FROM prompt_parts;

INSERT INTO usage (
  id, request_id, provider_id, timestamp, model, provider, provider_name,
  endpoint, user_path, cache_type, input_tokens, output_tokens, total_tokens,
  raw_data, input_cost, output_cost, total_cost, cost_source, costs_calculation_caveat
)
SELECT
  usage_id,
  request_id,
  provider_id,
  timestamp,
  model,
  provider,
  provider_name,
  endpoint,
  user_path,
  cache_type,
  input_tokens,
  output_tokens,
  total_tokens,
  CASE
    WHEN cache_type = 'exact' THEN json_object(
      'demo_seed', 1,
      'cache_story', 'exact local response cache hit',
      'locally_cached_tokens', total_tokens
    )
    WHEN cache_type = 'semantic' THEN json_object(
      'demo_seed', 1,
      'cache_story', 'semantic local response cache hit',
      'semantic_similarity', 0.88 + ((prompt_bucket % 12) / 100.0),
      'locally_cached_tokens', total_tokens
    )
    WHEN prompt_cache_hit = 1 AND provider = 'anthropic' THEN json_object(
      'demo_seed', 1,
      'cache_story', 'provider prompt cache read/write',
      'cache_read_input_tokens', prompt_cached_tokens,
      'cache_creation_input_tokens', prompt_cache_write_tokens
    )
    WHEN prompt_cache_hit = 1 AND provider = 'gemini' THEN json_object(
      'demo_seed', 1,
      'cache_story', 'provider prompt cache read',
      'cached_tokens', prompt_cached_tokens
    )
    WHEN prompt_cache_hit = 1 THEN json_object(
      'demo_seed', 1,
      'cache_story', 'provider prompt cache read',
      'prompt_cached_tokens', prompt_cached_tokens
    )
    ELSE json_object('demo_seed', 1, 'cache_story', 'uncached provider request')
  END AS raw_data,
  round((CASE
    WHEN cache_type IS NOT NULL THEN 0
    WHEN prompt_cache_hit = 1 THEN ((input_tokens - prompt_cached_tokens) * input_price + prompt_cached_tokens * input_price * 0.25) / 1000000.0
    ELSE input_tokens * input_price / 1000000.0
  END), 8) AS input_cost,
  round(CASE
    WHEN cache_type IS NOT NULL THEN 0
    ELSE output_tokens * output_price / 1000000.0
  END, 8) AS output_cost,
  round((CASE
    WHEN cache_type IS NOT NULL THEN 0
    WHEN prompt_cache_hit = 1 THEN (((input_tokens - prompt_cached_tokens) * input_price + prompt_cached_tokens * input_price * 0.25) / 1000000.0) + (output_tokens * output_price / 1000000.0)
    ELSE (input_tokens * input_price / 1000000.0) + (output_tokens * output_price / 1000000.0)
  END), 8) AS total_cost,
  CASE
    WHEN cache_type IS NOT NULL THEN 'demo_local_cache'
    WHEN prompt_cache_hit = 1 THEN 'demo_prompt_cache'
    ELSE 'demo_model_pricing'
  END AS cost_source,
  ''
FROM demo_generated;

INSERT INTO audit_logs (
  id, timestamp, duration_ns, requested_model, resolved_model, provider, provider_name,
  alias_used, workflow_version_id, cache_type, status_code, request_id, auth_key_id,
  auth_method, client_ip, method, path, user_path, stream, error_type, data
)
SELECT
  audit_id,
  timestamp,
  CASE WHEN cache_type IS NOT NULL THEN 8000000 + (token_noise % 12000000) ELSE 90000000 + (token_noise % 260000000) END,
  provider_name || '/' || model,
  provider_name || '/' || model,
  provider,
  provider_name,
  0,
  NULL,
  cache_type,
  CASE WHEN abs(token_noise / 131) % 1000 < 994 THEN 200 ELSE 500 END,
  request_id,
  NULL,
  'master_key',
  '127.0.0.1',
  'POST',
  endpoint,
  user_path,
  0,
  CASE WHEN abs(token_noise / 131) % 1000 < 994 THEN '' ELSE 'provider_error' END,
  json_object(
    'demo_seed', 1,
    'workflow_features', json_object(
      'cache', json('true'),
      'audit', json('true'),
      'usage', json('true'),
      'budget', json('true'),
      'guardrails', json('false'),
      'failover', json('true')
    ),
    'cache_type', cache_type,
    'cache_story', CASE
      WHEN cache_type = 'exact' THEN 'Exact response cache hit'
      WHEN cache_type = 'semantic' THEN 'Semantic response cache hit'
      WHEN prompt_cache_hit = 1 THEN 'Provider prompt cache telemetry'
      ELSE 'Uncached provider request'
    END,
    'request_body', json(CASE
      WHEN label IN ('chat-openai', 'chat-groq', 'chat-gemini', 'chat-bailian') THEN json_object(
        'model', provider_name || '/' || model,
        'messages', json_array(
          json_object('role', 'system', 'content', 'You are a concise assistant for internal demo traffic. Respect the user path and return actionable JSON when useful.'),
          json_object('role', 'user', 'content', 'Summarize daily gateway usage for ' || user_path || ' and call out cache savings, error spikes, and next actions.'),
          json_object('role', 'assistant', 'content', 'I will compare current traffic against the recent baseline and identify cost or latency anomalies.'),
          json_object('role', 'user', 'content', 'Use request id ' || request_id || ' and include provider ' || provider_name || '.')
        ),
        'temperature', round(0.15 + ((token_noise % 70) / 100.0), 2),
        'max_tokens', output_tokens,
        'stream', CASE WHEN slot_idx % 5 = 0 THEN json('true') ELSE json('false') END,
        'metadata', json_object(
          'demo', json('true'),
          'user_path', user_path,
          'cache_expected', CASE WHEN cache_type IS NOT NULL OR prompt_cache_hit = 1 THEN json('true') ELSE json('false') END
        )
      )
      WHEN label = 'responses' THEN json_object(
        'model', provider_name || '/' || model,
        'input', json_array(
          json_object('role', 'system', 'content', 'You are GoModel demo analysis worker.'),
          json_object('role', 'user', 'content', 'Create a short incident-style report for ' || user_path || ' using token totals and cache telemetry.')
        ),
        'instructions', 'Return sections named summary, observations, and recommendation.',
        'previous_response_id', CASE WHEN slot_idx > 0 AND slot_idx % 7 = 0 THEN '${prefix}-response-' || day_idx || '-' || (slot_idx - 1) ELSE NULL END,
        'max_output_tokens', output_tokens,
        'metadata', json_object('demo', json('true'), 'request_id', request_id)
      )
      WHEN label = 'messages' THEN json_object(
        'model', provider_name || '/' || model,
        'system', 'You help the engineering and sales teams reason about AI gateway telemetry.',
        'messages', json_array(
          json_object('role', 'user', 'content', json_array(
            json_object('type', 'text', 'text', 'Draft a weekly update for ' || user_path || ' with token volume, model mix, cache behavior, and budget risk.')
          ))
        ),
        'max_tokens', output_tokens,
        'temperature', round(0.10 + ((token_noise % 55) / 100.0), 2)
      )
      WHEN label = 'embeddings' THEN json_object(
        'model', provider_name || '/' || model,
        'input', json_array(
          'gateway usage dashboard prompt cache overview for ' || user_path,
          'semantic cache hit investigation request ' || request_id,
          'budget variance notes for provider ' || provider_name
        ),
        'encoding_format', 'float'
      )
      WHEN label = 'stt' THEN json_object(
        '__audio__', json('true'),
        'content_type', 'audio/mpeg',
        'bytes', 48000 + (token_noise % 180000),
        'stored', json('false'),
        'meta', json_object(
          'model', provider_name || '/' || model,
          'language', CASE WHEN token_noise % 4 = 0 THEN 'pl' ELSE 'en' END,
          'prompt', 'Demo meeting note for ' || user_path,
          'temperature', round((token_noise % 20) / 100.0, 2)
        )
      )
      WHEN label = 'tts' THEN json_object(
        'model', provider_name || '/' || model,
        'input', 'Read a concise dashboard summary for ' || user_path || ': tokens are trending up, prompt caching is active, and budgets remain under review.',
        'voice', CASE token_noise % 4 WHEN 0 THEN 'alloy' WHEN 1 THEN 'verse' WHEN 2 THEN 'coral' ELSE 'sage' END,
        'format', CASE WHEN token_noise % 3 = 0 THEN 'wav' ELSE 'mp3' END,
        'speed', round(0.90 + ((token_noise % 30) / 100.0), 2)
      )
      ELSE json_object('model', provider_name || '/' || model, 'input', 'Generated demo request')
    END),
    'response_body', json(CASE
      WHEN abs(token_noise / 131) % 1000 >= 994 THEN json_object(
        'error', json_object(
          'message', 'Synthetic upstream provider error for demo audit inspection.',
          'type', 'provider_error',
          'code', 'demo_provider_error',
          'param', 'model'
        )
      )
      WHEN label IN ('chat-openai', 'chat-groq', 'chat-gemini', 'chat-bailian') THEN json_object(
        'id', '${prefix}-response-' || day_idx || '-' || slot_idx,
        'object', 'chat.completion',
        'created', strftime('%s', timestamp),
        'model', provider_name || '/' || model,
        'choices', json_array(json_object(
          'index', 0,
          'finish_reason', 'stop',
          'message', json_object(
            'role', 'assistant',
            'content', 'Usage for ' || user_path || ' is healthy. Total tokens were ' || total_tokens || ', with cache mode ' || coalesce(cache_type, CASE WHEN prompt_cache_hit = 1 THEN 'prompt-cache' ELSE 'uncached' END) || '.'
          )
        )),
        'usage', json_object(
          'prompt_tokens', input_tokens,
          'completion_tokens', output_tokens,
          'total_tokens', total_tokens,
          'prompt_tokens_details', json_object('cached_tokens', prompt_cached_tokens)
        )
      )
      WHEN label = 'responses' THEN json_object(
        'id', '${prefix}-response-' || day_idx || '-' || slot_idx,
        'object', 'response',
        'status', 'completed',
        'model', provider_name || '/' || model,
        'output', json_array(json_object(
          'id', '${prefix}-msg-' || day_idx || '-' || slot_idx,
          'type', 'message',
          'role', 'assistant',
          'content', json_array(json_object(
            'type', 'output_text',
            'text', 'Summary: ' || user_path || ' generated ' || total_tokens || ' tokens. Observation: cache savings were ' || CASE WHEN cache_type IS NOT NULL OR prompt_cache_hit = 1 THEN 'visible' ELSE 'not present' END || '. Recommendation: keep monitoring budget drift.'
          ))
        )),
        'usage', json_object(
          'input_tokens', input_tokens,
          'output_tokens', output_tokens,
          'total_tokens', total_tokens,
          'input_tokens_details', json_object('cached_tokens', prompt_cached_tokens)
        )
      )
      WHEN label = 'messages' THEN json_object(
        'id', '${prefix}-response-' || day_idx || '-' || slot_idx,
        'type', 'message',
        'role', 'assistant',
        'model', provider_name || '/' || model,
        'content', json_array(json_object(
          'type', 'text',
          'text', 'Weekly update for ' || user_path || ': model usage is balanced, semantic cache checks are active, and budget burn is within demo limits.'
        )),
        'stop_reason', 'end_turn',
        'usage', json_object(
          'input_tokens', input_tokens,
          'output_tokens', output_tokens,
          'cache_read_input_tokens', prompt_cached_tokens,
          'cache_creation_input_tokens', prompt_cache_write_tokens
        )
      )
      WHEN label = 'embeddings' THEN json_object(
        'object', 'list',
        'model', provider_name || '/' || model,
        'data', json_array(
          json_object('object', 'embedding', 'index', 0, 'embedding', json_array(0.012, -0.034, 0.087, 0.003)),
          json_object('object', 'embedding', 'index', 1, 'embedding', json_array(-0.021, 0.045, 0.016, -0.008)),
          json_object('object', 'embedding', 'index', 2, 'embedding', json_array(0.005, 0.019, -0.042, 0.071))
        ),
        'usage', json_object('prompt_tokens', input_tokens, 'total_tokens', total_tokens)
      )
      WHEN label = 'stt' THEN json_object(
        'text', 'Synthetic transcript for ' || user_path || ': review gateway usage, cache hit rates, and budget status.',
        'duration_seconds', round(18.0 + ((token_noise % 2400) / 100.0), 2),
        'language', CASE WHEN token_noise % 4 = 0 THEN 'pl' ELSE 'en' END,
        'segments', json_array(
          json_object('id', 0, 'start', 0.00, 'end', 6.20, 'text', 'Review gateway usage and token volume.'),
          json_object('id', 1, 'start', 6.20, 'end', 12.80, 'text', 'Check prompt caching and semantic cache hits.'),
          json_object('id', 2, 'start', 12.80, 'end', 18.00, 'text', 'Confirm budgets for the user path.')
        )
      )
      WHEN label = 'tts' THEN json_object(
        '__audio__', json('true'),
        'content_type', CASE WHEN token_noise % 3 = 0 THEN 'audio/wav' ELSE 'audio/mpeg' END,
        'bytes', 32000 + (token_noise % 160000),
        'stored', json('false'),
        'meta', json_object(
          'model', provider_name || '/' || model,
          'voice', CASE token_noise % 4 WHEN 0 THEN 'alloy' WHEN 1 THEN 'verse' WHEN 2 THEN 'coral' ELSE 'sage' END,
          'format', CASE WHEN token_noise % 3 = 0 THEN 'wav' ELSE 'mp3' END
        )
      )
      ELSE json_object('id', '${prefix}-response-' || day_idx || '-' || slot_idx, 'object', label)
    END)
  )
FROM demo_generated;

DROP TABLE IF EXISTS temp.demo_budget_paths;
CREATE TEMP TABLE demo_budget_paths(user_path TEXT, daily_amount REAL, weekly_amount REAL, monthly_amount REAL);
INSERT INTO demo_budget_paths VALUES
  ('/', 420.00, 2500.00, 9500.00),
  ('/agents/team1', 82.00, 510.00, 1900.00),
  ('/agents/team1/research', 38.00, 225.00, 850.00),
  ('/agents/team2', 74.00, 455.00, 1700.00),
  ('/agents/team2/ops', 30.00, 180.00, 690.00),
  ('/engineering', 160.00, 980.00, 3700.00),
  ('/engineering/ai', 140.00, 850.00, 3200.00),
  ('/engineering/ai/mike', 92.00, 570.00, 2100.00),
  ('/engineering/ai/mike/evals', 54.00, 320.00, 1200.00),
  ('/engineering/ai/bot', 105.00, 650.00, 2450.00),
  ('/engineering/ai/bot/batch', 68.00, 420.00, 1600.00),
  ('/sales', 95.00, 585.00, 2200.00),
  ('/sales/john', 58.00, 355.00, 1350.00),
  ('/sales/john/prospects', 28.00, 165.00, 620.00);

WITH budget_rows AS (
  SELECT user_path, 86400 AS period_seconds, daily_amount AS amount FROM demo_budget_paths
  UNION ALL
  SELECT user_path, 604800 AS period_seconds, weekly_amount AS amount FROM demo_budget_paths
  UNION ALL
  SELECT user_path, 2592000 AS period_seconds, monthly_amount AS amount FROM demo_budget_paths
)
INSERT INTO budgets (user_path, period_seconds, amount, source, last_reset_at, created_at, updated_at)
SELECT
  user_path,
  period_seconds,
  amount,
  '${prefix}',
  strftime('%s', date(CASE WHEN '${end_date}' = '' THEN 'now' ELSE '${end_date}' END)),
  strftime('%s', 'now'),
  strftime('%s', 'now')
FROM budget_rows
WHERE true
ON CONFLICT(user_path, period_seconds) DO UPDATE SET
  amount = excluded.amount,
  source = excluded.source,
  last_reset_at = excluded.last_reset_at,
  updated_at = excluded.updated_at;

INSERT INTO budget_settings (key, value, updated_at)
VALUES
  ('daily_reset_hour', '0', strftime('%s', 'now')),
  ('daily_reset_minute', '0', strftime('%s', 'now')),
  ('weekly_reset_weekday', '1', strftime('%s', 'now')),
  ('weekly_reset_hour', '0', strftime('%s', 'now')),
  ('weekly_reset_minute', '0', strftime('%s', 'now')),
  ('monthly_reset_day', '1', strftime('%s', 'now')),
  ('monthly_reset_hour', '0', strftime('%s', 'now')),
  ('monthly_reset_minute', '0', strftime('%s', 'now'))
ON CONFLICT(key) DO UPDATE SET
  value = excluded.value,
  updated_at = excluded.updated_at;

COMMIT;

SELECT 'seed_prefix', '${prefix}';
SELECT 'date_range', min(date(REPLACE(timestamp, 'T', ' '))), max(date(REPLACE(timestamp, 'T', ' '))) FROM usage WHERE id GLOB '${prefix}-*';
SELECT 'usage_rows', count(*), coalesce(sum(total_tokens), 0) FROM usage WHERE id GLOB '${prefix}-*';
SELECT 'audit_rows', count(*) FROM audit_logs WHERE id GLOB '${prefix}-*';
SELECT 'budget_rows', count(*) FROM budgets WHERE source = '${prefix}';
SELECT 'cache_mix', coalesce(cache_type, CASE
  WHEN coalesce(json_extract(raw_data, '$.prompt_cached_tokens'), 0) > 0
    OR coalesce(json_extract(raw_data, '$.cached_tokens'), 0) > 0
    OR coalesce(json_extract(raw_data, '$.cache_read_input_tokens'), 0) > 0
  THEN 'prompt-cache'
  ELSE 'uncached'
END), count(*)
FROM usage
WHERE id GLOB '${prefix}-*'
GROUP BY 2
ORDER BY 2;
SELECT 'user_paths', count(DISTINCT user_path) FROM usage WHERE id GLOB '${prefix}-*';
SELECT 'daily_requests_min_max', min(rows), max(rows), round(avg(rows), 1)
FROM (
  SELECT date(REPLACE(timestamp, 'T', ' ')) AS day, count(*) AS rows
  FROM usage
  WHERE id GLOB '${prefix}-*'
  GROUP BY day
);
SQL

cat <<EOF

Seeded demo data into: $db_path
Prefix: $prefix

Open the dashboard and use a recent 90-day date range. To replace this generated
dataset, rerun the script with the same DEMO_SEED_PREFIX. To keep multiple
datasets side by side, use a different DEMO_SEED_PREFIX.
EOF

#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
db_path="${SQLITE_PATH:-data/gomodel.db}"
days="${DEMO_DAYS:-90}"
end_date="${DEMO_END_DATE:-}"
avg_requests="${DEMO_AVG_REQUESTS_PER_DAY:-850}"
max_requests="${DEMO_MAX_REQUESTS_PER_DAY:-1600}"
token_scale="${DEMO_TOKEN_SCALE:-3}"
exact_cache_pct="${DEMO_EXACT_CACHE_PCT:-12}"
semantic_cache_pct="${DEMO_SEMANTIC_CACHE_PCT:-7}"
prompt_cache_pct="${DEMO_PROMPT_CACHE_PCT:-28}"
rewrite_pct="${DEMO_REWRITE_PCT:-18}"
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
  DEMO_TOKEN_SCALE                Token volume multiplier (default: 3)
  DEMO_EXACT_CACHE_PCT            Local exact cache hit percentage (default: 12)
  DEMO_SEMANTIC_CACHE_PCT         Local semantic cache hit percentage (default: 7)
  DEMO_PROMPT_CACHE_PCT           Provider prompt-cache percentage (default: 28)
  DEMO_REWRITE_PCT                Eligible text requests with rewrite savings (default: 18)
  DEMO_SEED_PREFIX                Generated row/source prefix; reruns replace this prefix only (default: demo-generated)
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
require_int DEMO_TOKEN_SCALE "$token_scale"
require_int DEMO_EXACT_CACHE_PCT "$exact_cache_pct"
require_int DEMO_SEMANTIC_CACHE_PCT "$semantic_cache_pct"
require_int DEMO_PROMPT_CACHE_PCT "$prompt_cache_pct"
require_int DEMO_REWRITE_PCT "$rewrite_pct"

if (( days < 1 )); then
  echo "DEMO_DAYS must be at least 1" >&2
  exit 2
fi
if (( max_requests < avg_requests )); then
  echo "DEMO_MAX_REQUESTS_PER_DAY must be >= DEMO_AVG_REQUESTS_PER_DAY" >&2
  exit 2
fi
if (( token_scale < 1 || token_scale > 10 )); then
  echo "DEMO_TOKEN_SCALE must be between 1 and 10" >&2
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
if (( rewrite_pct > 60 )); then
  echo "DEMO_REWRITE_PCT must be <= 60" >&2
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
command -v openssl >/dev/null 2>&1 || {
  echo "openssl is required to generate demo API keys" >&2
  exit 127
}

demo_key_secret_team1="$(openssl rand -hex 24)"
demo_key_secret_engineering="$(openssl rand -hex 24)"
demo_key_secret_sales="$(openssl rand -hex 24)"
demo_key_hash_team1="$(printf '%s' "$demo_key_secret_team1" | openssl dgst -sha256 -r | awk '{print $1}')"
demo_key_hash_engineering="$(printf '%s' "$demo_key_secret_engineering" | openssl dgst -sha256 -r | awk '{print $1}')"
demo_key_hash_sales="$(printf '%s' "$demo_key_secret_sales" | openssl dgst -sha256 -r | awk '{print $1}')"
demo_key_redacted_team1="sk_gom_...${demo_key_secret_team1: -4}"
demo_key_redacted_engineering="sk_gom_...${demo_key_secret_engineering: -4}"
demo_key_redacted_sales="sk_gom_...${demo_key_secret_sales: -4}"

# A compact spoken fixture saying "Prompt caching is active." Keeping it
# separate from the SQL makes every generated STT upload and TTS response
# playable without requiring provider credentials during seeding.
demo_audio_mp3_base64="$(tr -d '\r\n' < "$script_dir/fixtures/demo-prompt-caching.mp3.base64")"
demo_audio_mp3_bytes="$(printf '%s' "$demo_audio_mp3_base64" | openssl base64 -d -A | wc -c | awk '{print $1}')"

mkdir -p "$(dirname "$db_path")"

sqlite3 "$db_path" "PRAGMA journal_mode = WAL;" >/dev/null

# Add columns introduced after the original demo seeder. Errors on fresh or
# already-migrated databases are benign; the schema below handles fresh files.
sqlite3 "$db_path" "ALTER TABLE usage ADD COLUMN labels JSON;" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE usage ADD COLUMN rewrite_tokens_saved INTEGER NOT NULL DEFAULT 0;" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE usage ADD COLUMN rewrite_cost_saved REAL;" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE mcp_servers ADD COLUMN display_name TEXT NOT NULL DEFAULT '';" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE auth_keys ADD COLUMN user_path TEXT;" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE auth_keys ADD COLUMN labels JSON;" 2>/dev/null || true

# make demo seeds before app startup, so migrate the rate-limit table here
# when the database predates scoped user-path/provider/model rules.
has_rate_limit_subject="$(sqlite3 "$db_path" "SELECT count(*) FROM pragma_table_info('rate_limits') WHERE name = 'subject';")"
has_rate_limit_user_path="$(sqlite3 "$db_path" "SELECT count(*) FROM pragma_table_info('rate_limits') WHERE name = 'user_path';")"
if [[ "$has_rate_limit_subject" == "0" && "$has_rate_limit_user_path" == "1" ]]; then
  sqlite3 "$db_path" <<'SQL'
.bail on
.timeout 10000
BEGIN IMMEDIATE;
ALTER TABLE rate_limits RENAME TO rate_limits_pre_scope;
CREATE TABLE rate_limits (
  scope TEXT NOT NULL DEFAULT 'user_path',
  subject TEXT NOT NULL,
  period_seconds INTEGER NOT NULL,
  max_requests INTEGER,
  max_tokens INTEGER,
  source TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (scope, subject, period_seconds)
);
INSERT INTO rate_limits (
  scope, subject, period_seconds, max_requests, max_tokens, source, created_at, updated_at
)
SELECT
  'user_path', user_path, period_seconds, max_requests, max_tokens, source, created_at, updated_at
FROM rate_limits_pre_scope;
DROP TABLE rate_limits_pre_scope;
DROP INDEX IF EXISTS idx_rate_limits_user_path;
COMMIT;
SQL
fi

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
  labels JSON,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  total_tokens INTEGER NOT NULL DEFAULT 0,
  rewrite_tokens_saved INTEGER NOT NULL DEFAULT 0,
  rewrite_cost_saved REAL,
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

CREATE TABLE IF NOT EXISTS rate_limits (
  scope TEXT NOT NULL DEFAULT 'user_path',
  subject TEXT NOT NULL,
  period_seconds INTEGER NOT NULL,
  max_requests INTEGER,
  max_tokens INTEGER,
  source TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (scope, subject, period_seconds)
);

CREATE TABLE IF NOT EXISTS mcp_servers (
  name TEXT PRIMARY KEY,
  display_name TEXT NOT NULL DEFAULT '',
  url TEXT NOT NULL DEFAULT '',
  transport TEXT NOT NULL DEFAULT 'http',
  headers TEXT NOT NULL DEFAULT '{}',
  description TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  allowed_tools TEXT NOT NULL DEFAULT '[]',
  disallowed_tools TEXT NOT NULL DEFAULT '[]',
  user_paths TEXT NOT NULL DEFAULT '[]',
  tool_timeout_seconds INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS auth_keys (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  user_path TEXT,
  labels JSON,
  redacted_value TEXT NOT NULL,
  secret_hash TEXT NOT NULL UNIQUE,
  enabled INTEGER NOT NULL DEFAULT 1,
  expires_at INTEGER,
  deactivated_at INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS virtual_models (
  source TEXT PRIMARY KEY,
  targets TEXT NOT NULL DEFAULT '[]',
  strategy TEXT NOT NULL DEFAULT '',
  provider_name TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  user_paths TEXT NOT NULL DEFAULT '[]',
  description TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS failover_rules (
  primary_model TEXT PRIMARY KEY,
  fallback_models TEXT NOT NULL DEFAULT '[]',
  enabled INTEGER NOT NULL DEFAULT 1,
  managed_source TEXT NOT NULL DEFAULT 'dashboard',
  created_at INTEGER NOT NULL,
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
CREATE INDEX IF NOT EXISTS idx_rate_limits_subject ON rate_limits(scope, subject);
CREATE INDEX IF NOT EXISTS idx_mcp_servers_enabled ON mcp_servers(enabled);
CREATE INDEX IF NOT EXISTS idx_mcp_servers_updated_at ON mcp_servers(updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_auth_keys_enabled ON auth_keys(enabled);
CREATE INDEX IF NOT EXISTS idx_auth_keys_created_at ON auth_keys(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_virtual_models_enabled ON virtual_models(enabled);
CREATE INDEX IF NOT EXISTS idx_virtual_models_updated_at ON virtual_models(updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_failover_rules_enabled ON failover_rules(enabled);
CREATE INDEX IF NOT EXISTS idx_failover_rules_updated_at ON failover_rules(updated_at DESC);

BEGIN IMMEDIATE;

DELETE FROM audit_logs WHERE id GLOB '${prefix}-*';
DELETE FROM usage WHERE id GLOB '${prefix}-*';
DELETE FROM budgets WHERE source = '${prefix}';
DELETE FROM rate_limits WHERE source = '${prefix}';
DELETE FROM auth_keys WHERE id GLOB '${prefix}-key-*';
DELETE FROM virtual_models WHERE description GLOB '${prefix}:*';
DELETE FROM failover_rules WHERE managed_source = '${prefix}';

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
  abs(random()) % 10000 AS rewrite_bucket,
  abs(random()) % 10000 AS label_bucket,
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
    (input_min + (token_noise % input_span)) * ${token_scale} AS input_tokens,
    (output_min + ((token_noise / 97) % output_span)) * ${token_scale} AS output_tokens
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
rewrite_decisions AS (
  SELECT
    *,
    CASE
      WHEN cache_type IS NULL
        AND label IN ('chat-openai', 'chat-groq', 'chat-gemini', 'chat-bailian', 'responses', 'messages')
        AND rewrite_bucket < (${rewrite_pct} * 100)
      THEN 1
      ELSE 0
    END AS rewrite_hit
  FROM cache_decisions
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
  FROM rewrite_decisions
)
SELECT
  *,
  input_tokens + output_tokens AS total_tokens,
  CASE
    WHEN rewrite_hit = 1 THEN CAST(input_tokens * (8 + ((token_noise / 17) % 23)) / 100 AS INTEGER)
    ELSE 0
  END AS rewrite_tokens_saved,
  strftime('%Y-%m-%dT%H:%M:%fZ', day || ' 00:00:00', '+' || second_of_day || ' seconds') AS timestamp,
  '${prefix}-usage-' || day_idx || '-' || slot_idx AS usage_id,
  '${prefix}-audit-' || day_idx || '-' || slot_idx AS audit_id,
  '${prefix}-req-' || day_idx || '-' || slot_idx AS request_id,
  '${prefix}-provider-' || day_idx || '-' || slot_idx AS provider_id
FROM prompt_parts;

INSERT INTO usage (
  id, request_id, provider_id, timestamp, model, provider, provider_name,
  endpoint, user_path, cache_type, labels, input_tokens, output_tokens, total_tokens,
  rewrite_tokens_saved, rewrite_cost_saved, raw_data,
  input_cost, output_cost, total_cost, cost_source, costs_calculation_caveat
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
  -- Request labels as extracted from tagging headers: roughly two thirds of
  -- traffic is labelled, some with two labels, the rest unlabelled (NULL).
  CASE
    WHEN label_bucket < 2500 THEN json_array('env:prod')
    WHEN label_bucket < 4000 THEN json_array('env:staging')
    WHEN label_bucket < 5200 THEN json_array('env:prod', 'batch')
    WHEN label_bucket < 6000 THEN json_array('experiment:rag-v2')
    WHEN label_bucket < 6600 THEN json_array('env:prod', 'priority:high')
    ELSE NULL
  END AS labels,
  input_tokens,
  output_tokens,
  total_tokens,
  rewrite_tokens_saved,
  CASE
    WHEN rewrite_tokens_saved > 0 THEN round(rewrite_tokens_saved * input_price / 1000000.0, 8)
    ELSE NULL
  END AS rewrite_cost_saved,
  json_patch(CASE
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
  END, CASE
    WHEN rewrite_hit = 1 THEN json_object(
      'rewrite_story', 'prompt context compressed before provider routing',
      'rewrite_tokens_saved', rewrite_tokens_saved,
      'rewriter', 'demo-context-compression'
    )
    ELSE json_object()
  END) AS raw_data,
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
  -- Each provider gets its own latency profile so the dashboard's provider
  -- latency chart shows distinct, plausible lines instead of overlapping noise.
  CASE WHEN cache_type IS NOT NULL THEN 8000000 + (token_noise % 12000000)
    ELSE CAST((90000000 + (token_noise % 260000000)) * CASE provider
      WHEN 'groq' THEN 0.45
      WHEN 'gemini' THEN 0.8
      WHEN 'anthropic' THEN 1.6
      WHEN 'bailian' THEN 2.2
      ELSE 1.0
    END AS INTEGER)
  END,
  provider_name || '/' || model,
  provider_name || '/' || model,
  provider,
  provider_name,
  0,
  NULL,
  cache_type,
  CASE
    WHEN abs(token_noise / 131) % 1000 < 985 THEN 200
    WHEN abs(token_noise / 131) % 1000 < 994 THEN 429
    ELSE 500
  END,
  request_id,
  NULL,
  'master_key',
  '127.0.0.1',
  'POST',
  endpoint,
  user_path,
  0,
  CASE
    WHEN abs(token_noise / 131) % 1000 < 985 THEN ''
    WHEN abs(token_noise / 131) % 1000 < 994 THEN 'rate_limit_exceeded'
    ELSE 'provider_error'
  END,
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
    'request_revisions', json(CASE
      WHEN rewrite_hit = 1 THEN json_array(json_object(
        'seq', 1,
        'rewriter', 'demo-context-compression',
        'bytes_before', 12000 + (token_noise % 28000),
        'bytes_after', CAST((12000 + (token_noise % 28000)) * (62 + (rewrite_bucket % 24)) / 100 AS INTEGER),
        'tokens_saved', rewrite_tokens_saved,
        'body', json_object(
          'model', provider_name || '/' || model,
          'input', 'Compressed context for ' || user_path || ': retain gateway totals, cache behavior, budget risk, and action items.',
          'metadata', json_object('demo', json('true'), 'rewritten', json('true'))
        ),
        'detail', json_object(
          'strategy', 'context-compression',
          'tokens_saved_estimate', rewrite_tokens_saved,
          'preserved_sections', json_array('usage', 'cache', 'budget', 'actions')
        )
      ))
      ELSE 'null'
    END),
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
        'bytes', ${demo_audio_mp3_bytes},
        'encoding', 'base64',
        'data', '${demo_audio_mp3_base64}',
        'stored', json('true'),
        'meta', json_object(
          'model', provider_name || '/' || model,
          'filename', 'prompt-caching-demo.mp3',
          'language', 'en',
          'prompt', 'Prompt caching is active.',
          'temperature', round((token_noise % 20) / 100.0, 2)
        )
      )
      WHEN label = 'tts' THEN json_object(
        'model', provider_name || '/' || model,
        'input', 'Prompt caching is active.',
        'voice', CASE token_noise % 4 WHEN 0 THEN 'alloy' WHEN 1 THEN 'verse' WHEN 2 THEN 'coral' ELSE 'sage' END,
        'format', 'mp3',
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
      WHEN abs(token_noise / 131) % 1000 >= 985 THEN json_object(
        'error', json_object(
          'message', 'Synthetic rate limit for demo audit inspection. Retry after a moment.',
          'type', 'rate_limit_exceeded',
          'code', 'demo_rate_limit',
          'param', NULL
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
        'text', 'Prompt caching is active.',
        'duration_seconds', 1.224,
        'language', 'en',
        'segments', json_array(
          json_object('id', 0, 'start', 0.00, 'end', 1.224, 'text', 'Prompt caching is active.')
        )
      )
      WHEN label = 'tts' THEN json_object(
        '__audio__', json('true'),
        'content_type', 'audio/mpeg',
        'bytes', ${demo_audio_mp3_bytes},
        'encoding', 'base64',
        'data', '${demo_audio_mp3_base64}',
        'stored', json('true'),
        'meta', json_object(
          'model', provider_name || '/' || model,
          'voice', CASE token_noise % 4 WHEN 0 THEN 'alloy' WHEN 1 THEN 'verse' WHEN 2 THEN 'coral' ELSE 'sage' END,
          'format', 'mp3',
          'transcript', 'Prompt caching is active.'
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

DROP TABLE IF EXISTS temp.demo_rate_limits;
CREATE TEMP TABLE demo_rate_limits(
  scope TEXT,
  subject TEXT,
  period_seconds INTEGER,
  max_requests INTEGER,
  max_tokens INTEGER
);
INSERT INTO demo_rate_limits VALUES
  ('user_path', '/agents/team1', 60, 900, 12000000),
  ('user_path', '/agents/team1', 0, 24, NULL),
  ('user_path', '/agents/team2', 3600, 8000, 80000000),
  ('user_path', '/engineering/ai', 86400, 30000, 420000000),
  ('user_path', '/engineering/ai/bot', 0, 48, NULL),
  ('user_path', '/sales', 60, 600, 5000000),
  ('provider', 'openai', 60, 1500, 30000000),
  ('provider', 'openai', 0, 80, NULL),
  ('provider', 'groq', 60, 2400, 40000000),
  ('provider', 'anthropic', 3600, 7500, 120000000),
  ('provider', 'gemini', 86400, 40000, 600000000),
  ('provider', 'bailian', 60, 1800, 25000000),
  ('model', 'openai/gpt-5-nano-2025-08-07', 60, 800, 16000000),
  ('model', 'anthropic/claude-haiku-4-5-20251001', 3600, 5000, 90000000),
  ('model', 'qwen-flash', 60, 1200, 18000000);

-- Do not take ownership of a rule the user already created for the same key.
INSERT OR IGNORE INTO rate_limits (
  scope, subject, period_seconds, max_requests, max_tokens, source, created_at, updated_at
)
SELECT
  scope,
  subject,
  period_seconds,
  max_requests,
  max_tokens,
  '${prefix}',
  strftime('%s', 'now'),
  strftime('%s', 'now')
FROM demo_rate_limits;

-- Disabled examples populate MCP management without making outbound calls.
DELETE FROM mcp_servers
WHERE name IN (
  'demo-' || substr(lower(replace('${prefix}', '.', '-')), 1, 44) || '-docs',
  'demo-' || substr(lower(replace('${prefix}', '.', '-')), 1, 44) || '-crm'
);
INSERT INTO mcp_servers (
  name, display_name, url, transport, headers, description, enabled,
  allowed_tools, disallowed_tools, user_paths, tool_timeout_seconds, created_at, updated_at
)
VALUES
  (
    'demo-' || substr(lower(replace('${prefix}', '.', '-')), 1, 44) || '-docs',
    'Engineering Docs',
    'https://mcp.demo.invalid/docs',
    'http',
    '{}',
    'Disabled demo server scoped to engineering documentation workflows.',
    0,
    json_array('search_docs', 'read_page'),
    '[]',
    json_array('/engineering'),
    20,
    strftime('%s', 'now'),
    strftime('%s', 'now')
  ),
  (
    'demo-' || substr(lower(replace('${prefix}', '.', '-')), 1, 44) || '-crm',
    'Sales CRM',
    'https://mcp.demo.invalid/crm',
    'sse',
    '{}',
    'Disabled demo server showing user-path and tool allow-list controls.',
    0,
    json_array('search_accounts', 'list_opportunities'),
    json_array('delete_account'),
    json_array('/sales'),
    15,
    strftime('%s', 'now'),
    strftime('%s', 'now')
  );

-- Active keys use fresh random secrets on every seed. Their plaintext values
-- are printed once below, matching the admin API's issue-once behavior.
INSERT INTO auth_keys (
  id, name, description, user_path, labels, redacted_value, secret_hash,
  enabled, expires_at, deactivated_at, created_at, updated_at
)
VALUES
  (
    '${prefix}-key-team1',
    'Agents Team 1',
    'Demo key for interactive agent and research requests.',
    '/agents/team1',
    json_array('env:demo', 'team:agents-1'),
    '${demo_key_redacted_team1}',
    '${demo_key_hash_team1}',
    1, NULL, NULL,
    strftime('%s', 'now'), strftime('%s', 'now')
  ),
  (
    '${prefix}-key-engineering',
    'Engineering AI',
    'Demo key for engineering evaluations and automated jobs.',
    '/engineering/ai',
    json_array('env:demo', 'team:engineering', 'priority:high'),
    '${demo_key_redacted_engineering}',
    '${demo_key_hash_engineering}',
    1, NULL, NULL,
    strftime('%s', 'now'), strftime('%s', 'now')
  ),
  (
    '${prefix}-key-sales',
    'Sales John',
    'Demo key for CRM summaries and sales-assistant traffic.',
    '/sales/john',
    json_array('env:demo', 'team:sales'),
    '${demo_key_redacted_sales}',
    '${demo_key_hash_sales}',
    1, NULL, NULL,
    strftime('%s', 'now'), strftime('%s', 'now')
  );

-- Named virtual models demonstrate aliases plus cost- and latency-oriented
-- target pools. Existing operator-owned aliases with these names win.
INSERT OR IGNORE INTO virtual_models (
  source, targets, strategy, provider_name, model, user_paths,
  description, enabled, created_at, updated_at
)
VALUES
  (
    'smart',
    json_array(
      json_object('provider', 'openai', 'model', 'gpt-5-nano-2025-08-07', 'weight', 1),
      json_object('provider', 'anthropic', 'model', 'claude-haiku-4-5-20251001', 'weight', 1),
      json_object('provider', 'gemini', 'model', 'gemini-2.5-flash-lite', 'weight', 1),
      json_object('provider', 'bailian', 'model', 'qwen-flash', 'weight', 1)
    ),
    'cost', '', '', '[]',
    '${prefix}: balanced cost-aware model pool',
    1, strftime('%s', 'now'), strftime('%s', 'now')
  ),
  (
    'normal',
    json_array(json_object('provider', 'openai', 'model', 'gpt-5-nano-2025-08-07', 'weight', 1)),
    'round_robin', '', '', '[]',
    '${prefix}: stable default model alias',
    1, strftime('%s', 'now'), strftime('%s', 'now')
  ),
  (
    'fast',
    json_array(
      json_object('provider', 'groq', 'model', 'llama-3.1-8b-instant', 'weight', 3),
      json_object('provider', 'gemini', 'model', 'gemini-2.5-flash-lite', 'weight', 2),
      json_object('provider', 'bailian', 'model', 'qwen-flash', 'weight', 2)
    ),
    'round_robin', '', '', '[]',
    '${prefix}: latency-oriented weighted model pool',
    1, strftime('%s', 'now'), strftime('%s', 'now')
  ),
  (
    'cheap',
    json_array(
      json_object('provider', 'openai', 'model', 'gpt-5-nano-2025-08-07', 'weight', 1),
      json_object('provider', 'groq', 'model', 'llama-3.1-8b-instant', 'weight', 1),
      json_object('provider', 'gemini', 'model', 'gemini-2.5-flash-lite', 'weight', 1),
      json_object('provider', 'bailian', 'model', 'qwen-flash', 'weight', 1)
    ),
    'cost', '', '', '[]',
    '${prefix}: lowest-cost available target',
    1, strftime('%s', 'now'), strftime('%s', 'now')
  ),
  (
    'quality',
    json_array(
      json_object('provider', 'anthropic', 'model', 'claude-haiku-4-5-20251001', 'weight', 2),
      json_object('provider', 'openai', 'model', 'gpt-5-nano-2025-08-07', 'weight', 1)
    ),
    'round_robin', '', '', json_array('/engineering', '/agents'),
    '${prefix}: quality-oriented pool scoped to engineering and agents',
    1, strftime('%s', 'now'), strftime('%s', 'now')
  );

-- Failover order intentionally crosses providers and mirrors the models used
-- by the generated traffic and aliases.
INSERT OR IGNORE INTO failover_rules (
  primary_model, fallback_models, enabled, managed_source, created_at, updated_at
)
VALUES
  (
    'openai/gpt-5-nano-2025-08-07',
    json_array('groq/llama-3.1-8b-instant', 'gemini/gemini-2.5-flash-lite', 'bailian/qwen-flash'),
    1, '${prefix}', strftime('%s', 'now'), strftime('%s', 'now')
  ),
  (
    'groq/llama-3.1-8b-instant',
    json_array('gemini/gemini-2.5-flash-lite', 'bailian/qwen-flash', 'openai/gpt-5-nano-2025-08-07'),
    1, '${prefix}', strftime('%s', 'now'), strftime('%s', 'now')
  ),
  (
    'gemini/gemini-2.5-flash-lite',
    json_array('groq/llama-3.1-8b-instant', 'bailian/qwen-flash', 'openai/gpt-5-nano-2025-08-07'),
    1, '${prefix}', strftime('%s', 'now'), strftime('%s', 'now')
  ),
  (
    'bailian/qwen-flash',
    json_array('groq/llama-3.1-8b-instant', 'gemini/gemini-2.5-flash-lite', 'openai/gpt-5-nano-2025-08-07'),
    1, '${prefix}', strftime('%s', 'now'), strftime('%s', 'now')
  ),
  (
    'anthropic/claude-haiku-4-5-20251001',
    json_array('openai/gpt-5-nano-2025-08-07', 'gemini/gemini-2.5-flash-lite', 'groq/llama-3.1-8b-instant'),
    1, '${prefix}', strftime('%s', 'now'), strftime('%s', 'now')
  );

COMMIT;

SELECT 'seed_prefix', '${prefix}';
SELECT 'date_range', min(date(REPLACE(timestamp, 'T', ' '))), max(date(REPLACE(timestamp, 'T', ' '))) FROM usage WHERE id GLOB '${prefix}-*';
SELECT 'usage_rows', count(*), coalesce(sum(total_tokens), 0) FROM usage WHERE id GLOB '${prefix}-*';
SELECT 'audit_rows', count(*) FROM audit_logs WHERE id GLOB '${prefix}-*';
SELECT 'budget_rows', count(*) FROM budgets WHERE source = '${prefix}';
SELECT 'rate_limit_rows', count(*) FROM rate_limits WHERE source = '${prefix}';
SELECT 'mcp_server_rows', count(*) FROM mcp_servers
WHERE name GLOB 'demo-' || substr(lower(replace('${prefix}', '.', '-')), 1, 44) || '-*';
SELECT 'auth_key_rows', count(*) FROM auth_keys WHERE id GLOB '${prefix}-key-*';
SELECT 'virtual_model_rows', count(*) FROM virtual_models WHERE description GLOB '${prefix}:*';
SELECT 'failover_rows', count(*) FROM failover_rules WHERE managed_source = '${prefix}';
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
SELECT 'rewritten_requests', count(*), coalesce(sum(rewrite_tokens_saved), 0), round(coalesce(sum(rewrite_cost_saved), 0), 4)
FROM usage
WHERE id GLOB '${prefix}-*' AND rewrite_tokens_saved > 0;
SELECT 'daily_requests_min_max', min(rows), max(rows), round(avg(rows), 1)
FROM (
  SELECT date(REPLACE(timestamp, 'T', ' ')) AS day, count(*) AS rows
  FROM usage
  WHERE id GLOB '${prefix}-*'
  GROUP BY day
);
SELECT 'daily_tokens_min_max', min(tokens), max(tokens), round(avg(tokens), 0)
FROM (
  SELECT date(REPLACE(timestamp, 'T', ' ')) AS day, sum(total_tokens) AS tokens
  FROM usage
  WHERE id GLOB '${prefix}-*'
  GROUP BY day
);
SQL

cat <<EOF

Seeded demo data into: $db_path
Prefix: $prefix

Generated demo API keys (replaced on every seed):
  /agents/team1    sk_gom_${demo_key_secret_team1}
  /engineering/ai sk_gom_${demo_key_secret_engineering}
  /sales/john      sk_gom_${demo_key_secret_sales}

Open the dashboard and use a recent 90-day date range. To replace this generated
dataset, rerun the script with the same DEMO_SEED_PREFIX. To keep multiple
datasets side by side, use a different DEMO_SEED_PREFIX.

Rate-limit counters are live process state and start at zero when GoModel starts.
Use the generated API keys to make requests and populate those counters.
EOF

#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
db_path="${SQLITE_PATH:-data/gomodel.db}"
days="${DEMO_DAYS:-90}"
end_date="${DEMO_END_DATE:-}"
avg_requests="${DEMO_AVG_REQUESTS_PER_DAY:-2000}"
max_requests="${DEMO_MAX_REQUESTS_PER_DAY:-3800}"
token_scale="${DEMO_TOKEN_SCALE:-3}"
exact_cache_pct="${DEMO_EXACT_CACHE_PCT:-12}"
semantic_cache_pct="${DEMO_SEMANTIC_CACHE_PCT:-7}"
prompt_cache_pct="${DEMO_PROMPT_CACHE_PCT:-28}"
rewrite_pct="${DEMO_REWRITE_PCT:-18}"
guardrail_edit_pct="${DEMO_GUARDRAIL_EDIT_PCT:-24}"
prefix="${DEMO_SEED_PREFIX:-demo-generated}"
seed_utc_epoch="$(date -u +%s)"
current_utc_second=$((seed_utc_epoch % 86400))

usage() {
  cat <<EOF
Usage: [env...] tools/seed-demo-data.sh

Environment:
  SQLITE_PATH                     SQLite DB path (default: data/gomodel.db)
  DEMO_DAYS                       Rolling day count (default: 90)
  DEMO_END_DATE                   End date YYYY-MM-DD (default: today UTC)
  DEMO_AVG_REQUESTS_PER_DAY       Average daily request count (default: 2000)
  DEMO_MAX_REQUESTS_PER_DAY       Upper slot cap per day (default: 3800)
  DEMO_TOKEN_SCALE                Token volume multiplier (default: 3)
  DEMO_EXACT_CACHE_PCT            Local exact cache hit percentage (default: 12)
  DEMO_SEMANTIC_CACHE_PCT         Local semantic cache hit percentage (default: 7)
  DEMO_PROMPT_CACHE_PCT           Provider prompt-cache percentage (default: 28)
  DEMO_REWRITE_PCT                Eligible text requests with rewrite savings (default: 18)
  DEMO_GUARDRAIL_EDIT_PCT         Guarded requests a redaction guardrail edits (default: 24)
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
require_int DEMO_GUARDRAIL_EDIT_PCT "$guardrail_edit_pct"

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
if (( guardrail_edit_pct > 80 )); then
  echo "DEMO_GUARDRAIL_EDIT_PCT must be <= 80" >&2
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

# Guardrail instances (plugin configurations) referenced by the seeded
# workflows below. Each config is written exactly as the matching built-in
# plugin parses it: the plugins reject unknown keys, so the demo doubles as a
# worked example of every built-in type. Single-quoted here so regular
# expression backslashes and $1 capture groups survive into the SQL heredoc.
guardrail_config_pii='{"rules":"[\\w.%+-]+@[\\w.-]+\\.[A-Za-z]{2,} => [redacted-email]\n\\+?[0-9][0-9 ().-]{9,}[0-9] => [redacted-phone]\nsk-[A-Za-z0-9]{20,} => [redacted-key]\n([0-9]{3})-([0-9]{2})-[0-9]{4} => $1-$2-XXXX","mode":"regex","case_insensitive":true,"roles":["user","assistant","tool"],"on_match":"replace"}'
guardrail_config_blocked_terms='{"rules":"(project|codename)[ -]nightingale => \nunreleased pricing sheet => ","mode":"regex","case_insensitive":true,"roles":["user"],"on_match":"block","message":"This request mentions material that may not leave the tenant. Remove it and try again.","block_status":403}'
guardrail_config_sales_tone='{"mode":"decorator","content":"Answer as a concise sales engineer. Never invent pricing, quote only figures present in the context, and end with one clear next step."}'
guardrail_config_headers='{"response_add":"X-GoModel-Demo: true","upstream_set":"X-Tenant: gomodel-demo","response_remove":"X-Powered-By"}'
guardrail_config_injection_judge='{"model":"openai/gpt-5-nano-2025-08-07","target":"last_user","action":"block","message":"This prompt was rejected as a prompt-injection attempt.","block_status":400,"on_unclear":"warn","max_tokens":128,"temperature":0}'
guardrail_config_quality_judge='{"model":"groq/llama-3.1-8b-instant","action":"warn","message":"The answer did not cite the retrieved context.","on_unclear":"allow","max_tokens":128,"temperature":0}'
guardrail_config_normalizer='{"model":"groq/llama-3.1-8b-instant","roles":["user"],"max_tokens":2048,"prompt":"Rewrite the message as one self-contained question. Keep every fact, identifier, and instruction, and return only the rewritten text."}'

# Workflow payloads. Guardrail references are written as @@name and expanded
# to the prefixed instance names below, so a generated workflow can never bind
# to an operator-owned guardrail that happens to share a plain name: that
# guardrail could be of a type the referencing phase does not support, and the
# workflow would then fail to compile and stop the gateway from starting.
# The stored hash is the SHA-256 of the expanded JSON, which is the encoding
# GoModel writes: schema version, canonical feature order, and steps sorted by
# phase, step, then ref.
workflow_payload_baseline_v1='{"schema_version":2,"features":{"cache":true,"audit":true,"usage":true,"budget":true,"guardrails":false,"failover":true}}'
workflow_payload_baseline='{"schema_version":2,"features":{"cache":true,"audit":true,"usage":true,"budget":true,"guardrails":true,"failover":true},"steps":[{"ref":"@@pii-redaction","phase":"prompt","step":0},{"ref":"@@gateway-headers","phase":"prompt","step":1},{"ref":"@@pii-redaction","phase":"response","step":0},{"ref":"@@gateway-headers","phase":"response","step":1}]}'
workflow_payload_sales='{"schema_version":2,"features":{"cache":true,"audit":true,"usage":true,"budget":true,"guardrails":true,"failover":true},"steps":[{"ref":"@@pii-redaction","phase":"prompt","step":0},{"ref":"@@sales-assistant-tone","phase":"prompt","step":1},{"ref":"@@answer-quality-judge","phase":"response","step":0}]}'
workflow_payload_agents='{"schema_version":2,"features":{"cache":true,"audit":true,"usage":true,"budget":true,"guardrails":true,"failover":true},"steps":[{"ref":"@@prompt-normalizer","phase":"prompt","step":0},{"ref":"@@blocked-terms","phase":"prompt","step":1}]}'
workflow_payload_batch='{"schema_version":2,"features":{"cache":true,"audit":true,"usage":true,"budget":true,"guardrails":true,"failover":false},"steps":[{"ref":"@@prompt-normalizer","phase":"prompt","step":0},{"ref":"@@pii-redaction","phase":"prompt","step":1}]}'
workflow_payload_anthropic='{"schema_version":2,"features":{"cache":false,"audit":true,"usage":true,"budget":true,"guardrails":true,"failover":true},"steps":[{"ref":"@@prompt-injection-judge","phase":"prompt","step":0},{"ref":"@@pii-redaction","phase":"prompt","step":1},{"ref":"@@pii-redaction","phase":"response","step":0}]}'

# Expand @@name guardrail references to the prefixed instance names.
expand_refs() {
  printf '%s' "${1//@@/${prefix}-}"
}

payload_hash() {
  printf '%s' "$1" | openssl dgst -sha256 -r | awk '{print $1}'
}

workflow_payload_baseline_v1="$(expand_refs "$workflow_payload_baseline_v1")"
workflow_payload_baseline="$(expand_refs "$workflow_payload_baseline")"
workflow_payload_sales="$(expand_refs "$workflow_payload_sales")"
workflow_payload_agents="$(expand_refs "$workflow_payload_agents")"
workflow_payload_batch="$(expand_refs "$workflow_payload_batch")"
workflow_payload_anthropic="$(expand_refs "$workflow_payload_anthropic")"

workflow_hash_baseline_v1="$(payload_hash "$workflow_payload_baseline_v1")"
workflow_hash_baseline="$(payload_hash "$workflow_payload_baseline")"
workflow_hash_sales="$(payload_hash "$workflow_payload_sales")"
workflow_hash_agents="$(payload_hash "$workflow_payload_agents")"
workflow_hash_batch="$(payload_hash "$workflow_payload_batch")"
workflow_hash_anthropic="$(payload_hash "$workflow_payload_anthropic")"

mkdir -p "$(dirname "$db_path")"

sqlite3 "$db_path" "PRAGMA journal_mode = WAL;" >/dev/null

# Add columns introduced after the original demo seeder. Errors on fresh or
# already-migrated databases are benign; the schema below handles fresh files.
sqlite3 "$db_path" "ALTER TABLE usage ADD COLUMN labels JSON;" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE usage ADD COLUMN rewrite_tokens_saved INTEGER NOT NULL DEFAULT 0;" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE usage ADD COLUMN rewrite_cost_saved REAL;" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE usage ADD COLUMN session_id TEXT;" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE mcp_servers ADD COLUMN display_name TEXT NOT NULL DEFAULT '';" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE auth_keys ADD COLUMN user_path TEXT;" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE auth_keys ADD COLUMN labels JSON;" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE auth_keys ADD COLUMN dashboard_access INTEGER NOT NULL DEFAULT 0;" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE audit_logs ADD COLUMN session_id TEXT;" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE virtual_models ADD COLUMN session_affinity TEXT NOT NULL DEFAULT '';" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE budgets ADD COLUMN source TEXT NOT NULL DEFAULT '';" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE budgets ADD COLUMN last_reset_at INTEGER;" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE budgets ADD COLUMN per_child INTEGER NOT NULL DEFAULT 0;" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE auth_keys ADD COLUMN allowed_models JSON;" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE guardrail_definitions ADD COLUMN user_path TEXT;" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE guardrail_definitions ADD COLUMN fail_mode TEXT NOT NULL DEFAULT '';" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE guardrail_definitions ADD COLUMN timeout_ms INTEGER NOT NULL DEFAULT 0;" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE workflow_versions ADD COLUMN scope_user_path TEXT;" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE workflow_versions ADD COLUMN managed_default INTEGER NOT NULL DEFAULT FALSE;" 2>/dev/null || true

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

# Keep demo seeding compatible with databases created before budget scopes.
# The application performs the same upgrade during startup, but the seeder can
# be run before the gateway starts.
has_budget_subject="$(sqlite3 "$db_path" "SELECT count(*) FROM pragma_table_info('budgets') WHERE name = 'subject';")"
has_budget_user_path="$(sqlite3 "$db_path" "SELECT count(*) FROM pragma_table_info('budgets') WHERE name = 'user_path';")"
if [[ "$has_budget_subject" == "0" && "$has_budget_user_path" == "1" ]]; then
  sqlite3 "$db_path" <<'SQL'
.bail on
.timeout 10000
BEGIN IMMEDIATE;
ALTER TABLE budgets RENAME TO budgets_pre_scope;
CREATE TABLE budgets (
  scope TEXT NOT NULL DEFAULT 'user_path',
  subject TEXT NOT NULL,
  per_child INTEGER NOT NULL DEFAULT 0,
  period_seconds INTEGER NOT NULL,
  amount REAL NOT NULL,
  source TEXT NOT NULL DEFAULT '',
  last_reset_at INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (scope, subject, period_seconds)
);
INSERT INTO budgets (scope, subject, period_seconds, amount, source, last_reset_at, created_at, updated_at)
SELECT 'user_path', user_path, period_seconds, amount, source, last_reset_at, created_at, updated_at
FROM budgets_pre_scope;
DROP TABLE budgets_pre_scope;
DROP INDEX IF EXISTS idx_budgets_user_path;
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
  session_id TEXT,
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
  session_id TEXT,
  stream INTEGER DEFAULT 0,
  error_type TEXT,
  data JSON
);

CREATE TABLE IF NOT EXISTS audit_log_attempts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  audit_log_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  kind TEXT NOT NULL,
  provider_type TEXT,
  provider_name TEXT,
  model TEXT,
  status_code INTEGER DEFAULT 0,
  success INTEGER DEFAULT FALSE,
  error_type TEXT,
  error_code TEXT,
  error_message TEXT,
  response_body TEXT,
  response_headers TEXT,
  started_at DATETIME,
  duration_ns INTEGER DEFAULT 0,
  UNIQUE(audit_log_id, seq),
  FOREIGN KEY(audit_log_id) REFERENCES audit_logs(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS budgets (
  scope TEXT NOT NULL DEFAULT 'user_path',
  subject TEXT NOT NULL,
  per_child INTEGER NOT NULL DEFAULT 0,
  period_seconds INTEGER NOT NULL,
  amount REAL NOT NULL,
  source TEXT NOT NULL DEFAULT '',
  last_reset_at INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (scope, subject, period_seconds)
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
  allowed_models JSON,
  dashboard_access INTEGER NOT NULL DEFAULT 0,
  redacted_value TEXT NOT NULL,
  secret_hash TEXT NOT NULL UNIQUE,
  enabled INTEGER NOT NULL DEFAULT 1,
  expires_at INTEGER,
  deactivated_at INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
  user_path TEXT PRIMARY KEY,
  allowed_models TEXT NOT NULL DEFAULT '[]',
  description TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS virtual_models (
  source TEXT PRIMARY KEY,
  targets TEXT NOT NULL DEFAULT '[]',
  strategy TEXT NOT NULL DEFAULT '',
  session_affinity TEXT NOT NULL DEFAULT '',
  provider_name TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  user_paths TEXT NOT NULL DEFAULT '[]',
  description TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS tagging_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS guardrail_definitions (
  name TEXT PRIMARY KEY,
  type TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  user_path TEXT,
  config JSON NOT NULL,
  fail_mode TEXT NOT NULL DEFAULT '',
  timeout_ms INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS workflow_versions (
  id TEXT PRIMARY KEY,
  scope_provider TEXT,
  scope_model TEXT,
  scope_user_path TEXT,
  scope_key TEXT NOT NULL,
  version INTEGER NOT NULL,
  active INTEGER NOT NULL DEFAULT TRUE,
  managed_default INTEGER NOT NULL DEFAULT FALSE,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  workflow_payload JSON NOT NULL,
  workflow_hash TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  CHECK (scope_provider IS NOT NULL OR scope_model IS NULL)
);

CREATE INDEX IF NOT EXISTS idx_usage_timestamp ON usage(timestamp);
CREATE INDEX IF NOT EXISTS idx_usage_request_id ON usage(request_id);
CREATE INDEX IF NOT EXISTS idx_usage_provider ON usage(provider);
CREATE INDEX IF NOT EXISTS idx_usage_provider_name ON usage(provider_name);
CREATE INDEX IF NOT EXISTS idx_usage_user_path ON usage(user_path);
CREATE INDEX IF NOT EXISTS idx_usage_cache_type ON usage(cache_type);
CREATE INDEX IF NOT EXISTS idx_usage_session_id ON usage(session_id);
CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_logs(timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_request_id ON audit_logs(request_id);
CREATE INDEX IF NOT EXISTS idx_audit_path ON audit_logs(path);
CREATE INDEX IF NOT EXISTS idx_audit_user_path ON audit_logs(user_path);
CREATE INDEX IF NOT EXISTS idx_audit_cache_type ON audit_logs(cache_type);
CREATE INDEX IF NOT EXISTS idx_audit_session_timestamp ON audit_logs(session_id, timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_attempts_log_seq ON audit_log_attempts(audit_log_id, seq);
CREATE INDEX IF NOT EXISTS idx_budgets_subject ON budgets(scope, subject);
CREATE INDEX IF NOT EXISTS idx_budgets_period_seconds ON budgets(period_seconds);
CREATE INDEX IF NOT EXISTS idx_rate_limits_subject ON rate_limits(scope, subject);
CREATE INDEX IF NOT EXISTS idx_mcp_servers_enabled ON mcp_servers(enabled);
CREATE INDEX IF NOT EXISTS idx_mcp_servers_updated_at ON mcp_servers(updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_auth_keys_enabled ON auth_keys(enabled);
CREATE INDEX IF NOT EXISTS idx_auth_keys_created_at ON auth_keys(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_virtual_models_enabled ON virtual_models(enabled);
CREATE INDEX IF NOT EXISTS idx_virtual_models_updated_at ON virtual_models(updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_guardrail_definitions_type ON guardrail_definitions(type);
CREATE INDEX IF NOT EXISTS idx_guardrail_definitions_updated_at ON guardrail_definitions(updated_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_workflow_versions_scope_version ON workflow_versions(scope_key, version);
CREATE UNIQUE INDEX IF NOT EXISTS idx_workflow_versions_active_scope ON workflow_versions(scope_key) WHERE active = TRUE;
CREATE INDEX IF NOT EXISTS idx_workflow_versions_active_created_at ON workflow_versions(active, created_at DESC);

BEGIN IMMEDIATE;

DELETE FROM audit_log_attempts WHERE audit_log_id GLOB '${prefix}-*';
DELETE FROM audit_logs WHERE id GLOB '${prefix}-*';
DELETE FROM usage WHERE id GLOB '${prefix}-*';
DELETE FROM budgets WHERE source = '${prefix}';
DELETE FROM rate_limits WHERE source = '${prefix}';
DELETE FROM auth_keys WHERE id GLOB '${prefix}-key-*';
DELETE FROM virtual_models WHERE description GLOB '${prefix}:*';
DELETE FROM users WHERE description GLOB '${prefix}:*';
DELETE FROM workflow_versions WHERE id GLOB '${prefix}-wf-*';
DELETE FROM guardrail_definitions WHERE description GLOB '${prefix}:*';

DROP TABLE IF EXISTS temp.demo_days;
CREATE TEMP TABLE demo_days AS
WITH RECURSIVE days(day_idx, day) AS (
  SELECT 0, date(
    CASE WHEN '${end_date}' = '' THEN date(${seed_utc_epoch}, 'unixepoch') ELSE '${end_date}' END,
    '-' || (${days} - 1) || ' days'
  )
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
  CAST(max(25, min(${max_requests}, round(
    ${avg_requests} * weekday_factor * trend_factor * seasonal_factor * noise_factor *
    CASE WHEN day = date(${seed_utc_epoch}, 'unixepoch')
      THEN max(0.02, (${current_utc_second} + 1) / 86400.0)
      ELSE 1.0
    END
  ))) AS INTEGER) AS request_count
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
  abs(random()) % 10000 AS session_bucket,
  abs(random()) % 10000 AS guardrail_bucket,
  abs(random()) % 10000 AS redact_bucket,
  abs(random()) % 10000 AS normalize_bucket,
  abs(random()) % 10000 AS outcome_bucket,
  abs(random()) % 10000 AS alias_bucket,
  abs(random()) % 10000 AS retarget_bucket,
  abs(random()) % CASE WHEN d.day = date(${seed_utc_epoch}, 'unixepoch')
    THEN ${current_utc_second} + 1
    ELSE 86400
  END AS second_of_day,
  abs(random()) AS token_noise
FROM demo_days d
JOIN demo_slots s ON s.slot_idx < d.request_count;

DROP TABLE IF EXISTS temp.demo_generated;
CREATE TEMP TABLE demo_generated AS
WITH pathed AS (
  SELECT
    b.*,
    p.user_path,
    p.min_bucket AS path_min
  FROM demo_random b
  JOIN demo_paths p ON b.path_bucket >= p.min_bucket AND b.path_bucket < p.max_bucket
),
-- Paths that carry a model allowlist (see the users table below) draw only
-- from the templates their policy permits, so the generated history never
-- shows a request the gateway would have rejected. A node's allowlist bounds
-- its whole subtree, which is why the two group rules apply to every path
-- under them. Offsets land inside the template ranges declared above.
routed AS (
  SELECT
    *,
    CASE
      -- gemini/ and openai/gpt-5-nano-2025-08-07: the two chat templates.
      WHEN user_path = '/agents/team1/research' THEN
        CASE retarget_bucket % 2
          WHEN 0 THEN template_bucket % 1700           -- chat-openai
          ELSE 3150 + (template_bucket % 1300)         -- chat-gemini
        END
      -- groq/llama-3.1-8b-instant and qwen-flash, which both bailian
      -- templates serve.
      WHEN user_path = '/engineering/ai/bot/batch' THEN
        CASE retarget_bucket % 3
          WHEN 0 THEN 1700 + (template_bucket % 1450)  -- chat-groq
          WHEN 1 THEN 4450 + (template_bucket % 1200)  -- chat-bailian
          ELSE 5650 + (template_bucket % 1200)         -- responses
        END
      -- /agents allows every provider except anthropic.
      WHEN user_path GLOB '/agents*' AND template_bucket >= 6850 AND template_bucket < 7850 THEN
        1700 + (template_bucket % 1450)                -- chat-groq
      -- /sales allows openai/ and anthropic/claude-haiku-4-5-20251001.
      WHEN user_path GLOB '/sales*' AND template_bucket >= 1700 AND template_bucket < 6850 THEN
        CASE retarget_bucket % 4
          WHEN 0 THEN template_bucket % 1700           -- chat-openai
          WHEN 1 THEN 6850 + (template_bucket % 1000)  -- messages
          WHEN 2 THEN 7850 + (template_bucket % 700)   -- embeddings
          ELSE 8550 + (template_bucket % 1400)         -- stt and tts
        END
      ELSE template_bucket
    END AS routed_template_bucket
  FROM pathed
),
chosen AS (
  SELECT
    b.*,
    t.min_bucket AS template_min,
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
    t.prompt_cache_eligible,
    -- The workflow a request resolves to, replicating the gateway's
    -- most-specific-scope match: the deepest user-path scope first, then a
    -- provider scope, then the global baseline.
    CASE
      WHEN b.user_path GLOB '/sales*' THEN '${prefix}-wf-sales'
      WHEN b.user_path GLOB '/agents/team1*' THEN '${prefix}-wf-agents'
      WHEN b.user_path GLOB '/engineering/ai/bot/batch*' THEN '${prefix}-wf-batch'
      WHEN b.user_path GLOB '/engineering/ai/mike*' THEN '${prefix}-wf-evals'
      WHEN t.provider = 'anthropic' THEN '${prefix}-wf-anthropic'
      ELSE '${prefix}-wf-global-v2'
    END AS workflow_version_id,
    CASE WHEN b.user_path GLOB '/engineering/ai/mike*' THEN 0 ELSE 1 END AS workflow_guardrails
  FROM routed b
  JOIN demo_templates t ON b.routed_template_bucket >= t.min_bucket AND b.routed_template_bucket < t.max_bucket
),
tokens AS (
  SELECT
    *,
    (input_min + (token_noise % input_span)) * ${token_scale} AS input_tokens,
    (output_min + ((token_noise / 97) % output_span)) * ${token_scale} AS output_tokens
  FROM chosen
),
-- Local cache eligibility also depends on the matched workflow: the
-- Anthropic policy turns the response cache off, so its traffic never reports
-- a local hit. Provider prompt caching is upstream telemetry and unaffected.
cache_decisions AS (
  SELECT
    *,
    CASE
      WHEN cache_workflow_eligible = 1 AND cache_bucket < (${exact_cache_pct} * 100) THEN 'exact'
      WHEN cache_workflow_eligible = 1 AND cache_bucket < ((${exact_cache_pct} + ${semantic_cache_pct}) * 100) THEN 'semantic'
      ELSE NULL
    END AS cache_type,
    CASE
      WHEN prompt_cache_eligible = 1
        AND NOT (cache_workflow_eligible = 1 AND cache_bucket < ((${exact_cache_pct} + ${semantic_cache_pct}) * 100))
        AND prompt_bucket < (${prompt_cache_pct} * 100)
      THEN 1
      ELSE 0
    END AS prompt_cache_hit
  FROM (
    SELECT
      *,
      CASE
        WHEN local_cache_eligible = 1 AND workflow_version_id != '${prefix}-wf-anthropic' THEN 1
        ELSE 0
      END AS cache_workflow_eligible
    FROM tokens
  )
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
),
-- Conversational requests in the same six-hour window on the same day, user
-- path, and endpoint share a session key. Grouping by endpoint (rather than
-- provider/model) lets a conversation retain its thread when a virtual model
-- routes a later turn elsewhere. The key stays below 2^31 so the hex mixing
-- below cannot overflow SQLite's 64-bit integer arithmetic.
session_keys AS (
  SELECT
    *,
    (((day_idx * 7 + (second_of_day / 21600)) * 131071 + path_min) * 8191 +
      CASE endpoint
        WHEN '/v1/chat/completions' THEN 1
        WHEN '/v1/responses' THEN 2
        WHEN '/v1/messages' THEN 3
        ELSE 0
      END) % 2147483647 AS session_key
  FROM prompt_parts
),
session_turns AS (
  SELECT
    *,
    row_number() OVER (PARTITION BY session_key ORDER BY second_of_day, slot_idx) AS session_turn,
    lag(slot_idx) OVER (PARTITION BY session_key ORDER BY second_of_day, slot_idx) AS previous_session_slot_idx
  FROM session_keys
),
-- Outcomes follow the request pipeline. Prompt guardrails run before the
-- response cache is consulted and the budget is enforced on dispatch, so a
-- locally cached answer can never be blocked or fail upstream; requests a
-- guardrail or a budget stopped are audited without a usage row, the way the
-- gateway records a request that produced no usage.
outcomes AS (
  SELECT
    *,
    CASE
      WHEN cache_type IS NOT NULL THEN 'ok'
      WHEN workflow_version_id = '${prefix}-wf-agents' AND guardrail_bucket < 120 THEN 'guardrail_blocked'
      WHEN workflow_version_id = '${prefix}-wf-anthropic' AND guardrail_bucket < 180 THEN 'guardrail_blocked'
      WHEN user_path IN ('/agents/team2/ops', '/sales/john/prospects') AND outcome_bucket < 250 THEN 'budget_blocked'
      WHEN abs(token_noise / 131) % 1000 >= 994 THEN 'provider_error'
      WHEN abs(token_noise / 131) % 1000 >= 985 THEN 'rate_limited'
      ELSE 'ok'
    END AS request_outcome
  FROM session_turns
)
SELECT
  *,
  CASE WHEN request_outcome IN ('budget_blocked', 'guardrail_blocked') THEN 0 ELSE 1 END AS usage_recorded,
  -- Prompt-phase guardrail effects. The prompt phase runs before the cache
  -- lookup and the budget check, so a cached or budget-stopped request still
  -- carries the steps that ran; a step that comes after a blocking one in its
  -- workflow never ran at all.
  CASE
    WHEN workflow_version_id = '${prefix}-wf-anthropic'
      AND request_outcome != 'guardrail_blocked'
      AND guardrail_bucket >= 180 AND guardrail_bucket < 720
    THEN 1
    ELSE 0
  END AS guardrail_warned,
  CASE
    WHEN workflow_version_id IN ('${prefix}-wf-agents', '${prefix}-wf-batch')
      AND normalize_bucket >= 3300
    THEN 1
    ELSE 0
  END AS guardrail_normalized,
  CASE
    WHEN workflow_version_id IN (
        '${prefix}-wf-global-v2', '${prefix}-wf-sales',
        '${prefix}-wf-batch', '${prefix}-wf-anthropic')
      AND request_outcome != 'guardrail_blocked'
      AND redact_bucket < (${guardrail_edit_pct} * 100)
    THEN 1
    ELSE 0
  END AS guardrail_redacted,
  -- Roughly a fifth of conversational traffic asks for a virtual model
  -- instead of a concrete one. The alias is drawn from those whose target
  -- pool contains the resolved provider, so requested and resolved models
  -- stay consistent; 'quality' is scoped to engineering and agents.
  CASE
    WHEN label NOT IN ('chat-openai', 'chat-groq', 'chat-gemini', 'chat-bailian', 'responses', 'messages') THEN NULL
    WHEN alias_bucket >= 2200 THEN NULL
    WHEN provider = 'anthropic' THEN CASE
      WHEN (user_path GLOB '/engineering*' OR user_path GLOB '/agents*') AND alias_bucket % 2 = 0 THEN 'quality'
      ELSE 'smart'
    END
    WHEN provider = 'groq' THEN CASE alias_bucket % 3
      WHEN 0 THEN 'fast'
      WHEN 1 THEN 'cheap'
      ELSE 'resilient'
    END
    WHEN provider = 'openai' THEN CASE alias_bucket % 4
      WHEN 0 THEN 'smart'
      WHEN 1 THEN 'cheap'
      WHEN 2 THEN 'normal'
      ELSE CASE
        WHEN user_path GLOB '/engineering*' OR user_path GLOB '/agents*' THEN 'quality'
        ELSE 'resilient'
      END
    END
    ELSE CASE alias_bucket % 4
      WHEN 0 THEN 'smart'
      WHEN 1 THEN 'fast'
      WHEN 2 THEN 'cheap'
      ELSE 'resilient'
    END
  END AS alias_source,
  input_tokens + output_tokens AS total_tokens,
  CASE
    WHEN rewrite_hit = 1 THEN CAST(input_tokens * (8 + ((token_noise / 17) % 23)) / 100 AS INTEGER)
    ELSE 0
  END AS rewrite_tokens_saved,
  -- Session ids use the detector's supported explicit body signal. Chat and
  -- Responses traffic always participates; most /v1/messages traffic carries
  -- a path-scoped client id, and the rest stays sessionless.
  CASE
    WHEN label IN ('chat-openai', 'chat-groq', 'chat-gemini', 'chat-bailian', 'responses') THEN
      'auto-' || printf('%08x%08x%08x%08x',
        (session_key * 2654435761 + 97) % 4294967296,
        (session_key * 2246822519 + 193) % 4294967296,
        (session_key * 3266489917 + 389) % 4294967296,
        (session_key * 668265263 + 769) % 4294967296)
    WHEN label = 'messages' AND session_bucket < 7000 THEN
      'scoped-' || printf('%08x%08x%08x%08x',
        (session_key * 2654435761 + 131) % 4294967296,
        (session_key * 2246822519 + 263) % 4294967296,
        (session_key * 3266489917 + 523) % 4294967296,
        (session_key * 668265263 + 1049) % 4294967296)
    ELSE NULL
  END AS session_id,
  strftime('%Y-%m-%dT%H:%M:%fZ', day || ' 00:00:00', '+' || second_of_day || ' seconds') AS timestamp,
  '${prefix}-usage-' || day_idx || '-' || slot_idx AS usage_id,
  '${prefix}-audit-' || day_idx || '-' || slot_idx AS audit_id,
  '${prefix}-req-' || day_idx || '-' || slot_idx AS request_id,
  '${prefix}-provider-' || day_idx || '-' || slot_idx AS provider_id
FROM outcomes;

INSERT INTO usage (
  id, request_id, provider_id, timestamp, model, provider, provider_name,
  endpoint, user_path, session_id, cache_type, labels, input_tokens, output_tokens, total_tokens,
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
  session_id,
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
-- Requests stopped by a budget or a prompt guardrail never reach a provider,
-- so they are audited without a usage row.
FROM demo_generated
WHERE usage_recorded = 1;

-- The audit request-revision chain, one row per step that changed the
-- request or objected to it: first the ingress rewriters, then the prompt
-- guardrails in the step order of the matched workflow, exactly as the
-- gateway records them. Steps that allowed a request without touching it
-- leave no entry, and each entry starts from the size the previous step
-- produced, so the chain reads as one request being reshaped.
DROP TABLE IF EXISTS temp.demo_revisions;
CREATE TEMP TABLE demo_revisions AS
WITH candidates AS (
  SELECT *, 8000 + (token_noise % 18000) AS bytes_original
  FROM demo_generated
  WHERE rewrite_hit = 1
     OR guardrail_normalized = 1
     OR guardrail_redacted = 1
     OR guardrail_warned = 1
     OR request_outcome = 'guardrail_blocked'
     OR workflow_version_id = '${prefix}-wf-sales'
),
after_rewriter AS (
  SELECT *, CASE
    WHEN rewrite_hit = 1 THEN CAST(bytes_original * (62 + (rewrite_bucket % 24)) / 100 AS INTEGER)
    ELSE bytes_original
  END AS bytes_rewritten
  FROM candidates
),
after_normalizer AS (
  SELECT *, CASE
    WHEN guardrail_normalized = 1 THEN CAST(bytes_rewritten * (74 + (normalize_bucket % 19)) / 100 AS INTEGER)
    ELSE bytes_rewritten
  END AS bytes_normalized
  FROM after_rewriter
),
after_redaction AS (
  SELECT *, CASE
    WHEN guardrail_redacted = 1 THEN bytes_normalized - (12 + (redact_bucket % 40))
    ELSE bytes_normalized
  END AS bytes_redacted
  FROM after_normalizer
),
chain AS (
  SELECT *, CASE
    WHEN workflow_version_id = '${prefix}-wf-sales' THEN bytes_redacted + 214
    ELSE bytes_redacted
  END AS bytes_toned
  FROM after_redaction
)
SELECT
  audit_id,
  1 AS step_order,
  json_object(
    'rewriter', 'demo-context-compression',
    'bytes_before', bytes_original,
    'bytes_after', bytes_rewritten,
    'tokens_saved', rewrite_tokens_saved,
    'body', json_object(
      'model', coalesce(alias_source, provider_name || '/' || model),
      'input', 'Compressed context for ' || user_path || ': retain gateway totals, cache behavior, budget risk, and action items.',
      'metadata', json_object('demo', json('true'), 'rewritten', json('true'))
    ),
    'detail', json_object(
      'strategy', 'context-compression',
      'tokens_saved_estimate', rewrite_tokens_saved,
      'preserved_sections', json_array('usage', 'cache', 'budget', 'actions')
    )
  ) AS revision
FROM chain
WHERE rewrite_hit = 1

UNION ALL
SELECT
  audit_id,
  2,
  json_object(
    'rewriter', '${prefix}-prompt-normalizer',
    'bytes_before', bytes_rewritten,
    'bytes_after', bytes_normalized,
    'body', json_object(
      'model', coalesce(alias_source, provider_name || '/' || model),
      'messages', json_array(json_object(
        'role', 'user',
        'content', 'Normalized turn ' || session_turn || ' for ' || user_path || ': one self-contained question with the run identifiers kept.'
      ))
    ),
    'detail', json_object('phase', 'prompt', 'action', 'allow')
  )
FROM chain
WHERE guardrail_normalized = 1

UNION ALL
SELECT
  audit_id,
  3,
  json_object(
    'rewriter', '${prefix}-prompt-injection-judge',
    'bytes_before', bytes_normalized,
    'bytes_after', bytes_normalized,
    'no_change', json('true'),
    'detail', json_object(
      'phase', 'prompt',
      'action', 'warn',
      'code', 'guardrail_warning',
      'message', 'The judge verdict was unclear; the request was allowed and flagged.'
    )
  )
FROM chain
WHERE guardrail_warned = 1

UNION ALL
SELECT
  audit_id,
  4,
  json_object(
    'rewriter', '${prefix}-pii-redaction',
    'bytes_before', bytes_normalized,
    'bytes_after', bytes_redacted,
    'detail', json_object('phase', 'prompt', 'action', 'allow')
  )
FROM chain
WHERE guardrail_redacted = 1

UNION ALL
SELECT
  audit_id,
  5,
  json_object(
    'rewriter', '${prefix}-sales-assistant-tone',
    'bytes_before', bytes_redacted,
    'bytes_after', bytes_toned,
    'detail', json_object('phase', 'prompt', 'action', 'allow')
  )
FROM chain
WHERE workflow_version_id = '${prefix}-wf-sales'

UNION ALL
SELECT
  audit_id,
  6,
  json_object(
    'rewriter', CASE WHEN workflow_version_id = '${prefix}-wf-agents' THEN '${prefix}-blocked-terms' ELSE '${prefix}-prompt-injection-judge' END,
    'bytes_before', bytes_normalized,
    'bytes_after', bytes_normalized,
    'no_change', json('true'),
    'detail', json_object(
      'phase', 'prompt',
      'action', 'block',
      'code', 'guardrail_blocked',
      'message', CASE
        WHEN workflow_version_id = '${prefix}-wf-agents'
          THEN 'This request mentions material that may not leave the tenant. Remove it and try again.'
        ELSE 'This prompt was rejected as a prompt-injection attempt.'
      END
    )
  )
FROM chain
WHERE request_outcome = 'guardrail_blocked';

DROP TABLE IF EXISTS temp.demo_revision_arrays;
CREATE TEMP TABLE demo_revision_arrays AS
SELECT audit_id, json_group_array(json(json_set(revision, '$.seq', rn))) AS revisions
FROM (
  SELECT
    audit_id,
    revision,
    row_number() OVER (PARTITION BY audit_id ORDER BY step_order) AS rn
  FROM demo_revisions
  ORDER BY audit_id, step_order
)
GROUP BY audit_id;

CREATE UNIQUE INDEX temp.idx_demo_revision_arrays ON demo_revision_arrays(audit_id);

INSERT INTO audit_logs (
  id, timestamp, duration_ns, requested_model, resolved_model, provider, provider_name,
  alias_used, workflow_version_id, cache_type, status_code, request_id, auth_key_id,
  auth_method, client_ip, method, path, user_path, session_id, stream, error_type, data
)
SELECT
  audit_id,
  timestamp,
  -- Each provider gets its own latency profile so the dashboard's provider
  -- latency chart shows distinct, plausible lines instead of overlapping noise.
  -- Requests stopped early are fast; a guardrail that calls a judge model, or
  -- a prompt rewritten by the normalizer, adds that model call to the total.
  CASE
    WHEN request_outcome = 'budget_blocked' THEN 900000 + (token_noise % 2200000)
    WHEN request_outcome = 'guardrail_blocked' AND workflow_version_id = '${prefix}-wf-agents'
      THEN 1400000 + (token_noise % 3600000)
    WHEN request_outcome = 'guardrail_blocked' THEN 190000000 + (token_noise % 520000000)
    ELSE
      CASE
        WHEN cache_type IS NOT NULL THEN 8000000 + (token_noise % 12000000)
        ELSE CAST((90000000 + (token_noise % 260000000)) * CASE provider
          WHEN 'groq' THEN 0.45
          WHEN 'gemini' THEN 0.8
          WHEN 'anthropic' THEN 1.6
          WHEN 'bailian' THEN 2.2
          ELSE 1.0
        END AS INTEGER)
      END
      -- The prompt phase runs before the cache lookup, so a guardrail that
      -- calls a model is part of the total even on a cache hit.
      + CASE WHEN guardrail_normalized = 1 THEN 210000000 + (token_noise % 260000000) ELSE 0 END
      + CASE WHEN guardrail_warned = 1 THEN 170000000 + (token_noise % 240000000) ELSE 0 END
  END,
  coalesce(alias_source, provider_name || '/' || model),
  provider_name || '/' || model,
  provider,
  provider_name,
  CASE WHEN alias_source IS NOT NULL THEN 1 ELSE 0 END,
  workflow_version_id,
  cache_type,
  CASE request_outcome
    WHEN 'budget_blocked' THEN 429
    WHEN 'guardrail_blocked' THEN
      CASE WHEN workflow_version_id = '${prefix}-wf-agents' THEN 403 ELSE 400 END
    WHEN 'rate_limited' THEN 429
    WHEN 'provider_error' THEN 500
    ELSE 200
  END,
  request_id,
  -- The generated demo keys own the traffic of their user paths, except where
  -- a key could not have made the request: the sales key allows only openai/,
  -- so its path's other providers were reached with the master key.
  CASE
    WHEN user_path GLOB '/agents/team1*' THEN '${prefix}-key-team1'
    WHEN user_path GLOB '/engineering/ai*' THEN '${prefix}-key-engineering'
    WHEN user_path GLOB '/sales/john*' AND provider = 'openai' THEN '${prefix}-key-sales'
    ELSE NULL
  END,
  CASE
    WHEN user_path GLOB '/agents/team1*'
      OR user_path GLOB '/engineering/ai*'
      OR (user_path GLOB '/sales/john*' AND provider = 'openai') THEN 'api_key'
    ELSE 'master_key'
  END,
  '10.42.' || (abs(token_noise / 7) % 6) || '.' || ((abs(token_noise / 13) % 250) + 2),
  'POST',
  endpoint,
  user_path,
  session_id,
  -- Streaming is what the client asked for, so a request a guardrail or a
  -- budget stopped keeps the flag its body carries.
  CASE
    WHEN label IN ('chat-openai', 'chat-groq', 'chat-gemini', 'chat-bailian', 'responses', 'messages')
      AND slot_idx % 5 = 0
    THEN 1
    ELSE 0
  END,
  CASE request_outcome
    WHEN 'budget_blocked' THEN 'rate_limit_error'
    WHEN 'guardrail_blocked' THEN 'invalid_request_error'
    WHEN 'rate_limited' THEN 'rate_limit_error'
    WHEN 'provider_error' THEN 'provider_error'
    ELSE ''
  END,
  json_object(
    'demo_seed', 1,
    -- The effective features of the workflow this request matched.
    'workflow_features', json_object(
      'cache', json(CASE WHEN workflow_version_id = '${prefix}-wf-anthropic' THEN 'false' ELSE 'true' END),
      'audit', json('true'),
      'usage', json('true'),
      'budget', json('true'),
      'guardrails', json(CASE WHEN workflow_guardrails = 1 THEN 'true' ELSE 'false' END),
      'failover', json(CASE WHEN workflow_version_id = '${prefix}-wf-batch' THEN 'false' ELSE 'true' END)
    ),
    'error_code', CASE request_outcome
      WHEN 'budget_blocked' THEN 'budget_exceeded'
      WHEN 'guardrail_blocked' THEN 'guardrail_blocked'
      WHEN 'rate_limited' THEN 'demo_rate_limit'
      WHEN 'provider_error' THEN 'demo_provider_error'
      ELSE NULL
    END,
    'error_message', CASE request_outcome
      WHEN 'budget_blocked' THEN 'daily budget for ' || user_path || ' exceeded'
      WHEN 'guardrail_blocked' THEN
        CASE
          WHEN workflow_version_id = '${prefix}-wf-agents'
            THEN 'This request mentions material that may not leave the tenant. Remove it and try again.'
          ELSE 'This prompt was rejected as a prompt-injection attempt.'
        END
      WHEN 'rate_limited' THEN 'Synthetic rate limit for demo audit inspection. Retry after a moment.'
      WHEN 'provider_error' THEN 'Synthetic upstream provider error for demo audit inspection.'
      ELSE NULL
    END,
    -- Gateway-raised errors name no provider; a budget stop names the budget
    -- checker, which is how the dashboard tells them apart from upstream ones.
    'error_provider', CASE request_outcome
      WHEN 'budget_blocked' THEN 'budget'
      WHEN 'rate_limited' THEN provider_name
      WHEN 'provider_error' THEN provider_name
      ELSE NULL
    END,
    -- The failover snapshot names the target the redirect moved to; it is
    -- present exactly when the seeded attempt trail shows a failover.
    -- Only a workflow with failover on can redirect, so the batch workflow's
    -- failures carry no target and get no failover attempt below.
    'failover', json(CASE
      WHEN request_outcome = 'provider_error' AND workflow_version_id != '${prefix}-wf-batch'
      THEN json_object('target_model', CASE provider
        WHEN 'openai' THEN 'groq/llama-3.1-8b-instant'
        WHEN 'groq' THEN 'gemini/gemini-2.5-flash-lite'
        WHEN 'gemini' THEN 'groq/llama-3.1-8b-instant'
        WHEN 'bailian' THEN 'groq/llama-3.1-8b-instant'
        ELSE 'openai/gpt-5-nano-2025-08-07'
      END)
      ELSE 'null'
    END),
    'cache_type', cache_type,
    'cache_story', CASE
      WHEN cache_type = 'exact' THEN 'Exact response cache hit'
      WHEN cache_type = 'semantic' THEN 'Semantic response cache hit'
      WHEN prompt_cache_hit = 1 THEN 'Provider prompt cache telemetry'
      ELSE 'Uncached provider request'
    END,
    -- One entry per rewriter or prompt guardrail that changed the request or
    -- objected to it, in application order (see temp.demo_revisions).
    'request_revisions', json(coalesce(revisions, 'null')),
    'request_body', json(CASE
      WHEN label IN ('chat-openai', 'chat-groq', 'chat-gemini', 'chat-bailian') THEN json_object(
        'model', coalesce(alias_source, provider_name || '/' || model),
        'session_id', session_id,
        'messages', json_array(
          json_object('role', 'system', 'content', 'You are a concise assistant for internal demo traffic. Respect the user path and return actionable JSON when useful.'),
          json_object('role', 'user', 'content', 'Summarize daily gateway usage for ' || user_path || ' and call out cache savings, error spikes, and next actions.'),
          json_object('role', 'assistant', 'content', 'I will compare current traffic against the recent baseline and identify cost or latency anomalies.'),
          json_object('role', 'user', 'content', 'Continue session turn ' || session_turn || ' for ' || user_path || '; include provider ' || provider_name || '.')
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
        'model', coalesce(alias_source, provider_name || '/' || model),
        'session_id', session_id,
        'input', json_array(
          json_object('role', 'system', 'content', 'You are GoModel demo analysis worker.'),
          json_object('role', 'user', 'content', 'Create a short incident-style report for ' || user_path || ' on session turn ' || session_turn || ' using token totals and cache telemetry.')
        ),
        'instructions', 'Return sections named summary, observations, and recommendation.',
        'stream', CASE WHEN slot_idx % 5 = 0 THEN json('true') ELSE json('false') END,
        'previous_response_id', CASE
          WHEN previous_session_slot_idx IS NOT NULL THEN '${prefix}-response-' || day_idx || '-' || previous_session_slot_idx
          ELSE NULL
        END,
        'max_output_tokens', output_tokens,
        'metadata', json_object('demo', json('true'), 'request_id', request_id)
      )
      WHEN label = 'messages' THEN json_object(
        'model', coalesce(alias_source, provider_name || '/' || model),
        'session_id', session_id,
        'system', 'You help the engineering and sales teams reason about AI gateway telemetry.',
        'messages', json_array(
          json_object('role', 'user', 'content', json_array(
            json_object('type', 'text', 'text', 'Continue weekly update turn ' || session_turn || ' for ' || user_path || ' with token volume, model mix, cache behavior, and budget risk.')
          ))
        ),
        'max_tokens', output_tokens,
        'stream', CASE WHEN slot_idx % 5 = 0 THEN json('true') ELSE json('false') END,
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
      WHEN request_outcome = 'budget_blocked' THEN json_object(
        'error', json_object(
          'message', 'daily budget for ' || user_path || ' exceeded',
          'type', 'rate_limit_error',
          'code', 'budget_exceeded',
          'param', NULL
        )
      )
      WHEN request_outcome = 'guardrail_blocked' THEN json_object(
        'error', json_object(
          'message', CASE
            WHEN workflow_version_id = '${prefix}-wf-agents'
              THEN 'This request mentions material that may not leave the tenant. Remove it and try again.'
            ELSE 'This prompt was rejected as a prompt-injection attempt.'
          END,
          'type', 'invalid_request_error',
          'code', 'guardrail_blocked',
          'param', NULL
        )
      )
      WHEN request_outcome = 'provider_error' THEN json_object(
        'error', json_object(
          'message', 'Synthetic upstream provider error for demo audit inspection.',
          'type', 'provider_error',
          'code', 'demo_provider_error',
          'param', 'model'
        )
      )
      WHEN request_outcome = 'rate_limited' THEN json_object(
        'error', json_object(
          'message', 'Synthetic rate limit for demo audit inspection. Retry after a moment.',
          'type', 'rate_limit_error',
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
            'content', 'Session turn ' || session_turn || ' for ' || user_path || ' is healthy. Total tokens were ' || total_tokens || ', with cache mode ' || coalesce(cache_type, CASE WHEN prompt_cache_hit = 1 THEN 'prompt-cache' ELSE 'uncached' END) || '.'
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
            'text', 'Turn ' || session_turn || ': ' || user_path || ' generated ' || total_tokens || ' tokens. Observation: cache savings were ' || CASE WHEN cache_type IS NOT NULL OR prompt_cache_hit = 1 THEN 'visible' ELSE 'not present' END || '. Recommendation: keep monitoring budget drift.'
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
          'text', 'Weekly update turn ' || session_turn || ' for ' || user_path || ': model usage is balanced, semantic cache checks are active, and budget burn is within demo limits.'
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
FROM demo_generated LEFT JOIN demo_revision_arrays USING (audit_id);

-- Attempt trails feed the request drawer's failover pips. Failed entries show
-- a primary attempt plus a cross-provider failover that also failed (the
-- failover order matches the seeded 'resilient' virtual model); a slice of successful
-- uncached entries shows a rate-limited primary followed by a clean retry.
INSERT INTO audit_log_attempts (
  audit_log_id, seq, kind, provider_type, provider_name, model,
  status_code, success, error_type, error_code, error_message,
  started_at, duration_ns
)
SELECT
  audit_id, 1, 'primary', provider, provider_name, model,
  502, 0, 'provider_error', 'upstream_error',
  'Synthetic upstream 502 from ' || provider_name || ' for demo failover inspection.',
  timestamp, 90000000 + (token_noise % 140000000)
FROM demo_generated
WHERE request_outcome = 'provider_error'
UNION ALL
SELECT
  audit_id, 2, 'failover',
  CASE provider WHEN 'openai' THEN 'groq' WHEN 'groq' THEN 'gemini' WHEN 'gemini' THEN 'groq' WHEN 'bailian' THEN 'groq' ELSE 'openai' END,
  CASE provider WHEN 'openai' THEN 'groq' WHEN 'groq' THEN 'gemini' WHEN 'gemini' THEN 'groq' WHEN 'bailian' THEN 'groq' ELSE 'openai' END,
  CASE provider
    WHEN 'openai' THEN 'llama-3.1-8b-instant'
    WHEN 'groq' THEN 'gemini-2.5-flash-lite'
    WHEN 'gemini' THEN 'llama-3.1-8b-instant'
    WHEN 'bailian' THEN 'llama-3.1-8b-instant'
    ELSE 'gpt-5-nano-2025-08-07'
  END,
  500, 0, 'provider_error', 'upstream_error',
  'Synthetic failover attempt also failed upstream for demo inspection.',
  strftime('%Y-%m-%dT%H:%M:%fZ', day || ' 00:00:00', '+' || (second_of_day + 1) || ' seconds'),
  80000000 + (token_noise % 110000000)
FROM demo_generated
WHERE request_outcome = 'provider_error'
  AND workflow_version_id != '${prefix}-wf-batch'
  AND (day != date(${seed_utc_epoch}, 'unixepoch') OR second_of_day < ${current_utc_second});

INSERT INTO audit_log_attempts (
  audit_log_id, seq, kind, provider_type, provider_name, model,
  status_code, success, error_type, error_code, error_message,
  started_at, duration_ns
)
SELECT
  audit_id, 1, 'primary', provider, provider_name, model,
  429, 0, 'rate_limit_exceeded', 'rate_limited',
  'Provider returned 429 for demo inspection; the gateway retried with backoff.',
  timestamp, 40000000 + (token_noise % 50000000)
FROM demo_generated
WHERE request_outcome = 'ok' AND cache_type IS NULL AND token_noise % 37 = 0
UNION ALL
SELECT
  audit_id, 2, 'retry', provider, provider_name, model,
  200, 1, NULL, NULL, NULL,
  strftime('%Y-%m-%dT%H:%M:%fZ', day || ' 00:00:00', '+' || (second_of_day + 1) || ' seconds'),
  90000000 + (token_noise % 200000000)
FROM demo_generated
WHERE request_outcome = 'ok'
  AND cache_type IS NULL
  AND token_noise % 37 = 0
  AND (day != date(${seed_utc_epoch}, 'unixepoch') OR second_of_day < ${current_utc_second});

-- Guardrail instances are plugin configurations the workflows below
-- reference by name. Types cover every built-in plugin, so the Guardrails
-- page shows a working example of each. INSERT OR IGNORE keeps an
-- operator-created instance of the same name untouched; the demo rows are
-- recognised by their prefixed description and replaced on reseed.
-- The two model-backed instances fail open so a demo gateway without the
-- matching provider key keeps serving traffic instead of rejecting it.
INSERT OR IGNORE INTO guardrail_definitions (
  name, type, description, user_path, config, fail_mode, timeout_ms, created_at, updated_at
)
VALUES
  (
    '${prefix}-pii-redaction', 'string_replace',
    '${prefix}: masks emails, phone numbers, keys, and SSNs in prompts and answers',
    NULL, '${guardrail_config_pii}', 'closed', 0,
    ${seed_utc_epoch} - 5184000, ${seed_utc_epoch} - 604800
  ),
  (
    '${prefix}-blocked-terms', 'string_replace',
    '${prefix}: rejects prompts naming confidential programs',
    NULL, '${guardrail_config_blocked_terms}', 'closed', 0,
    ${seed_utc_epoch} - 4320000, ${seed_utc_epoch} - 1209600
  ),
  (
    '${prefix}-sales-assistant-tone', 'system_prompt',
    '${prefix}: decorates the system prompt for sales conversations',
    '/sales', '${guardrail_config_sales_tone}', '', 0,
    ${seed_utc_epoch} - 3888000, ${seed_utc_epoch} - 259200
  ),
  (
    '${prefix}-gateway-headers', 'header_edit',
    '${prefix}: tags responses and forwards the tenant header upstream',
    NULL, '${guardrail_config_headers}', 'open', 0,
    ${seed_utc_epoch} - 3456000, ${seed_utc_epoch} - 259200
  ),
  (
    '${prefix}-prompt-injection-judge', 'llm_judge',
    '${prefix}: blocks prompt-injection attempts before the provider call',
    NULL, '${guardrail_config_injection_judge}', 'open', 4000,
    ${seed_utc_epoch} - 2592000, ${seed_utc_epoch} - 86400
  ),
  (
    '${prefix}-answer-quality-judge', 'llm_judge',
    '${prefix}: flags answers that ignore the retrieved context',
    '/sales', '${guardrail_config_quality_judge}', 'open', 6000,
    ${seed_utc_epoch} - 1728000, ${seed_utc_epoch} - 86400
  ),
  (
    '${prefix}-prompt-normalizer', 'llm_based_altering',
    '${prefix}: rewrites agent and batch prompts into self-contained questions',
    NULL, '${guardrail_config_normalizer}', 'open', 8000,
    ${seed_utc_epoch} - 2160000, ${seed_utc_epoch} - 172800
  );

-- Workflows are immutable versions selected per request by the most specific
-- matching scope. The seeded set covers a global baseline (with one
-- superseded version so the history view is not empty), three user-path
-- scopes, and one provider scope, and attaches the guardrails above across
-- the prompt and response phases. Active rows for these scopes are retired
-- first, exactly as publishing a new version does; the gateway leaves an
-- operator-authored active global workflow in place, so the managed default
-- is not recreated over this one.
UPDATE workflow_versions
SET active = FALSE
WHERE active = TRUE
  AND scope_key IN (
    'global', 'path:/sales', 'path:/agents/team1',
    'path:/engineering/ai/bot/batch', 'path:/engineering/ai/mike', 'provider:anthropic'
  );

INSERT INTO workflow_versions (
  id, scope_provider, scope_model, scope_user_path, scope_key, version,
  active, managed_default, name, description, workflow_payload, workflow_hash, created_at
)
SELECT '${prefix}-wf-global-v1', NULL, NULL, NULL, 'global',
  (SELECT coalesce(max(version), 0) FROM workflow_versions WHERE scope_key = 'global') + 1,
  FALSE, FALSE, 'Gateway baseline',
  'Superseded: cache, audit, usage, budgets, and failover without guardrails',
  '${workflow_payload_baseline_v1}', '${workflow_hash_baseline_v1}', ${seed_utc_epoch} - 5184000;

INSERT INTO workflow_versions (
  id, scope_provider, scope_model, scope_user_path, scope_key, version,
  active, managed_default, name, description, workflow_payload, workflow_hash, created_at
)
SELECT '${prefix}-wf-global-v2', NULL, NULL, NULL, 'global',
  (SELECT coalesce(max(version), 0) FROM workflow_versions WHERE scope_key = 'global') + 1,
  TRUE, FALSE, 'Gateway baseline',
  'Default for unscoped traffic: redaction and response tagging on every request',
  '${workflow_payload_baseline}', '${workflow_hash_baseline}', ${seed_utc_epoch} - 2592000;

INSERT INTO workflow_versions (
  id, scope_provider, scope_model, scope_user_path, scope_key, version,
  active, managed_default, name, description, workflow_payload, workflow_hash, created_at
)
SELECT '${prefix}-wf-sales', NULL, NULL, '/sales', 'path:/sales',
  (SELECT coalesce(max(version), 0) FROM workflow_versions WHERE scope_key = 'path:/sales') + 1,
  TRUE, FALSE, 'Sales assistant',
  'Redacts customer data, sets the sales tone, and judges answer quality',
  '${workflow_payload_sales}', '${workflow_hash_sales}', ${seed_utc_epoch} - 1728000;

INSERT INTO workflow_versions (
  id, scope_provider, scope_model, scope_user_path, scope_key, version,
  active, managed_default, name, description, workflow_payload, workflow_hash, created_at
)
SELECT '${prefix}-wf-agents', NULL, NULL, '/agents/team1', 'path:/agents/team1',
  (SELECT coalesce(max(version), 0) FROM workflow_versions WHERE scope_key = 'path:/agents/team1') + 1,
  TRUE, FALSE, 'Agent research',
  'Normalizes long agent prompts and rejects confidential program names',
  '${workflow_payload_agents}', '${workflow_hash_agents}', ${seed_utc_epoch} - 2160000;

INSERT INTO workflow_versions (
  id, scope_provider, scope_model, scope_user_path, scope_key, version,
  active, managed_default, name, description, workflow_payload, workflow_hash, created_at
)
SELECT '${prefix}-wf-batch', NULL, NULL, '/engineering/ai/bot/batch', 'path:/engineering/ai/bot/batch',
  (SELECT coalesce(max(version), 0) FROM workflow_versions WHERE scope_key = 'path:/engineering/ai/bot/batch') + 1,
  TRUE, FALSE, 'Batch jobs',
  'Prompt normalization and redaction for batch traffic, failover off',
  '${workflow_payload_batch}', '${workflow_hash_batch}', ${seed_utc_epoch} - 1296000;

INSERT INTO workflow_versions (
  id, scope_provider, scope_model, scope_user_path, scope_key, version,
  active, managed_default, name, description, workflow_payload, workflow_hash, created_at
)
SELECT '${prefix}-wf-evals', NULL, NULL, '/engineering/ai/mike', 'path:/engineering/ai/mike',
  (SELECT coalesce(max(version), 0) FROM workflow_versions WHERE scope_key = 'path:/engineering/ai/mike') + 1,
  TRUE, FALSE, 'Evaluation harness',
  'Guardrails off so evaluation prompts reach the model exactly as written',
  '${workflow_payload_baseline_v1}', '${workflow_hash_baseline_v1}', ${seed_utc_epoch} - 1036800;

INSERT INTO workflow_versions (
  id, scope_provider, scope_model, scope_user_path, scope_key, version,
  active, managed_default, name, description, workflow_payload, workflow_hash, created_at
)
SELECT '${prefix}-wf-anthropic', 'anthropic', NULL, NULL, 'provider:anthropic',
  (SELECT coalesce(max(version), 0) FROM workflow_versions WHERE scope_key = 'provider:anthropic') + 1,
  TRUE, FALSE, 'Anthropic policy',
  'Judges prompts for injection and redacts both sides; response cache off',
  '${workflow_payload_anthropic}', '${workflow_hash_anthropic}', ${seed_utc_epoch} - 864000;

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
INSERT INTO budgets (scope, subject, per_child, period_seconds, amount, source, last_reset_at, created_at, updated_at)
SELECT
  'user_path',
  user_path,
  0,
  period_seconds,
  amount,
  '${prefix}',
  strftime('%s', CASE
    WHEN '${end_date}' = '' THEN date(${seed_utc_epoch}, 'unixepoch')
    ELSE date('${end_date}')
  END),
  ${seed_utc_epoch},
  ${seed_utc_epoch}
FROM budget_rows
WHERE true
ON CONFLICT(scope, subject, period_seconds) DO UPDATE SET
  per_child = excluded.per_child,
  amount = excluded.amount,
  source = excluded.source,
  last_reset_at = excluded.last_reset_at,
  updated_at = excluded.updated_at;

-- Label-scoped budgets cap spend for one request label (as seeded on usage
-- rows) across all user paths. Per-child quota templates are not seeded: they
-- require a GoModel Pro entitlement, and the gateway refuses to start when a
-- per-child budget exists without it.
INSERT INTO budgets (scope, subject, per_child, period_seconds, amount, source, last_reset_at, created_at, updated_at)
VALUES
  ('label', 'env:prod', 0, 86400, 260.00, '${prefix}',
    strftime('%s', CASE WHEN '${end_date}' = '' THEN date(${seed_utc_epoch}, 'unixepoch') ELSE date('${end_date}') END),
    ${seed_utc_epoch}, ${seed_utc_epoch}),
  ('label', 'env:prod', 0, 2592000, 6800.00, '${prefix}',
    strftime('%s', CASE WHEN '${end_date}' = '' THEN date(${seed_utc_epoch}, 'unixepoch') ELSE date('${end_date}') END),
    ${seed_utc_epoch}, ${seed_utc_epoch}),
  ('label', 'experiment:rag-v2', 0, 604800, 120.00, '${prefix}',
    strftime('%s', CASE WHEN '${end_date}' = '' THEN date(${seed_utc_epoch}, 'unixepoch') ELSE date('${end_date}') END),
    ${seed_utc_epoch}, ${seed_utc_epoch})
ON CONFLICT(scope, subject, period_seconds) DO UPDATE SET
  per_child = excluded.per_child,
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
-- are printed once below, matching the admin API's issue-once behavior. The
-- engineering key carries dashboard_access so the per-key admin-API toggle
-- shows both states, and the sales key carries a model allowlist so the
-- per-key access controls show a restricted key next to unrestricted ones.
INSERT INTO auth_keys (
  id, name, description, user_path, labels, allowed_models, dashboard_access,
  redacted_value, secret_hash, enabled, expires_at, deactivated_at, created_at, updated_at
)
VALUES
  (
    '${prefix}-key-team1',
    'Agents Team 1',
    'Demo key for interactive agent and research requests.',
    '/agents/team1',
    json_array('env:demo', 'team:agents-1'),
    NULL,
    0,
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
    NULL,
    1,
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
    json_array('openai/'),
    0,
    '${demo_key_redacted_sales}',
    '${demo_key_hash_sales}',
    1, NULL, NULL,
    strftime('%s', 'now'), strftime('%s', 'now')
  );

-- Per-user-path access policies (the Users page). A node's non-empty allowlist
-- bounds its whole subtree: children and keys can narrow but never widen it.
-- Selectors show every canonical form: provider-wide "provider/", exact
-- "provider/model", and model-wide "model". An empty list keeps the node
-- unrestricted while still carrying a description. INSERT OR IGNORE leaves
-- operator-created policies for the same path untouched. The generated
-- traffic is routed to respect every list here (see the routed CTE above).
INSERT OR IGNORE INTO users (user_path, allowed_models, description, created_at, updated_at)
VALUES
  (
    '/agents',
    json_array('openai/', 'groq/', 'gemini/', 'bailian/'),
    '${prefix}: agent teams stay on fast, low-cost chat providers',
    strftime('%s', 'now'), strftime('%s', 'now')
  ),
  (
    '/agents/team1/research',
    json_array('gemini/', 'openai/gpt-5-nano-2025-08-07'),
    '${prefix}: research narrows its group allowlist to evaluation models',
    strftime('%s', 'now'), strftime('%s', 'now')
  ),
  (
    '/engineering',
    '[]',
    '${prefix}: engineering group node without model restrictions',
    strftime('%s', 'now'), strftime('%s', 'now')
  ),
  (
    '/engineering/ai/bot/batch',
    json_array('groq/llama-3.1-8b-instant', 'qwen-flash'),
    '${prefix}: batch jobs are pinned to the cheapest chat models',
    strftime('%s', 'now'), strftime('%s', 'now')
  ),
  (
    '/sales',
    json_array('openai/', 'anthropic/claude-haiku-4-5-20251001'),
    '${prefix}: sales uses OpenAI plus Claude Haiku for summaries',
    strftime('%s', 'now'), strftime('%s', 'now')
  );

-- Tagging rules explain where the labels on generated usage rows come from.
-- INSERT OR IGNORE keeps operator-edited rules untouched on reseed.
INSERT OR IGNORE INTO tagging_settings (key, value, updated_at)
VALUES (
  'headers',
  '[{"header":"X-Demo-Labels","delimiter":","},{"header":"X-Demo-Experiment","prefix":"exp-","do_not_pass":true}]',
  strftime('%s', 'now')
);

-- Named virtual models demonstrate aliases plus cost- and latency-oriented
-- target pools. Existing operator-owned aliases with these names win. Session
-- affinity stays on its default (sticky) except for 'cheap', which opts out so
-- the editor's "Session keeping" checkbox shows both states.
INSERT OR IGNORE INTO virtual_models (
  source, targets, strategy, session_affinity, provider_name, model, user_paths,
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
    'cost', '', '', '', '[]',
    '${prefix}: balanced cost-aware model pool',
    1, strftime('%s', 'now'), strftime('%s', 'now')
  ),
  (
    'normal',
    json_array(json_object('provider', 'openai', 'model', 'gpt-5-nano-2025-08-07', 'weight', 1)),
    'round_robin', '', '', '', '[]',
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
    'round_robin', '', '', '', '[]',
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
    'cost', 'false', '', '', '[]',
    '${prefix}: lowest-cost available target',
    1, strftime('%s', 'now'), strftime('%s', 'now')
  ),
  (
    'quality',
    json_array(
      json_object('provider', 'anthropic', 'model', 'claude-haiku-4-5-20251001', 'weight', 2),
      json_object('provider', 'openai', 'model', 'gpt-5-nano-2025-08-07', 'weight', 1)
    ),
    'round_robin', '', '', '', json_array('/engineering', '/agents'),
    '${prefix}: quality-oriented pool scoped to engineering and agents',
    1, strftime('%s', 'now'), strftime('%s', 'now')
  );

-- A failover-strategy redirect: targets are a priority list, and the order
-- intentionally crosses providers, mirroring the models used by the generated
-- traffic. Existing operator-owned virtual models with this name win.
INSERT OR IGNORE INTO virtual_models (
  source, targets, strategy, session_affinity, provider_name, model, user_paths,
  description, enabled, created_at, updated_at
)
VALUES
  (
    'resilient',
    json_array(
      json_object('provider', 'openai', 'model', 'gpt-5-nano-2025-08-07'),
      json_object('provider', 'groq', 'model', 'llama-3.1-8b-instant'),
      json_object('provider', 'gemini', 'model', 'gemini-2.5-flash-lite'),
      json_object('provider', 'bailian', 'model', 'qwen-flash')
    ),
    'failover', '', '', '', '[]',
    '${prefix}: priority-ordered failover pool',
    1, strftime('%s', 'now'), strftime('%s', 'now')
  );

COMMIT;

SELECT 'seed_prefix', '${prefix}';
SELECT 'date_range', min(date(REPLACE(timestamp, 'T', ' '))), max(date(REPLACE(timestamp, 'T', ' '))) FROM usage WHERE id GLOB '${prefix}-*';
SELECT 'usage_rows', count(*), coalesce(sum(total_tokens), 0) FROM usage WHERE id GLOB '${prefix}-*';
SELECT 'audit_rows', count(*) FROM audit_logs WHERE id GLOB '${prefix}-*';
SELECT 'session_threads', count(DISTINCT session_id), count(*) FROM audit_logs
WHERE id GLOB '${prefix}-*' AND session_id IS NOT NULL;
SELECT 'session_thread_sizes', min(n), max(n), round(avg(n), 1)
FROM (
  SELECT count(*) AS n FROM audit_logs
  WHERE id GLOB '${prefix}-*' AND session_id IS NOT NULL
  GROUP BY session_id
);
SELECT 'attempt_rows', count(*) FROM audit_log_attempts WHERE audit_log_id GLOB '${prefix}-*';
SELECT 'budget_rows', count(*) FROM budgets WHERE source = '${prefix}';
SELECT 'budget_scope_mix', scope, count(*)
FROM budgets WHERE source = '${prefix}' GROUP BY 2 ORDER BY 2;
SELECT 'rate_limit_rows', count(*) FROM rate_limits WHERE source = '${prefix}';
SELECT 'user_policy_rows', count(*) FROM users WHERE description GLOB '${prefix}:*';
SELECT 'mcp_server_rows', count(*) FROM mcp_servers
WHERE name GLOB 'demo-' || substr(lower(replace('${prefix}', '.', '-')), 1, 44) || '-*';
SELECT 'auth_key_rows', count(*) FROM auth_keys WHERE id GLOB '${prefix}-key-*';
SELECT 'virtual_model_rows', count(*) FROM virtual_models WHERE description GLOB '${prefix}:*';
SELECT 'guardrail_rows', count(*) FROM guardrail_definitions WHERE description GLOB '${prefix}:*';
SELECT 'guardrail_types', type, count(*) FROM guardrail_definitions
WHERE description GLOB '${prefix}:*' GROUP BY 2 ORDER BY 2;
SELECT 'workflow_rows', count(*), sum(active) FROM workflow_versions WHERE id GLOB '${prefix}-wf-*';
SELECT 'workflow_traffic', workflow_version_id, count(*)
FROM audit_logs WHERE id GLOB '${prefix}-*' GROUP BY 2 ORDER BY 3 DESC;
SELECT 'request_revisions', count(*) FROM audit_logs
WHERE id GLOB '${prefix}-*' AND json_extract(data, '$.request_revisions') IS NOT NULL;
SELECT 'audit_status_mix', status_code, count(*)
FROM audit_logs WHERE id GLOB '${prefix}-*' GROUP BY 2 ORDER BY 2;
SELECT 'blocked_requests', coalesce(json_extract(data, '$.error_code'), 'none'), count(*)
FROM audit_logs WHERE id GLOB '${prefix}-*' AND status_code >= 400 GROUP BY 2 ORDER BY 2;
SELECT 'alias_requests', count(*) FROM audit_logs WHERE id GLOB '${prefix}-*' AND alias_used = 1;
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

Audit entries carry session ids, so the Audit Logs page groups them into
threads by default ("Group by session"); failed and retried requests carry
attempt trails visible in the request drawer.

Every request resolves to one of the seeded workflows, so the request drawer
renders its pipeline. Guardrails cover all built-in plugin types and are
attached across the prompt and response phases; requests a prompt guardrail
blocked, and requests a budget stopped, are audited without a usage row, the
way the gateway records a request that never reached a provider. Start the
gateway with GUARDRAILS_ENABLED=true (make demo does) to see the Guardrails
and Plugins pages live; the two model-backed guardrails fail open, so a
gateway without those provider keys keeps serving traffic.

The Users page shows per-user-path model allowlists (groups bound their
subtrees; /agents/team1/research and /engineering/ai/bot/batch narrow their
groups), and the Sales key carries a per-key allowlist on top of the /sales
policy. Budgets include label-scoped examples (env:prod, experiment:rag-v2)
next to plain user-path limits.

Rate-limit counters are live process state and start at zero when GoModel starts.
Use the generated API keys to make requests and populate those counters.
EOF

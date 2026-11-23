#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
db_path="${SQLITE_PATH:-data/gomodel.db}"
days="${DEMO_DAYS:-90}"
end_date="${DEMO_END_DATE:-}"
avg_requests="${DEMO_AVG_REQUESTS_PER_DAY:-600}"
max_requests="${DEMO_MAX_REQUESTS_PER_DAY:-1500}"
token_scale="${DEMO_TOKEN_SCALE:-1}"
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
  DEMO_AVG_REQUESTS_PER_DAY       Requests on an average weekday (default: 600)
  DEMO_MAX_REQUESTS_PER_DAY       Upper slot cap per day (default: 1500)
  DEMO_TOKEN_SCALE                Token volume multiplier (default: 1)
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

demo_key_secret_support="$(openssl rand -hex 24)"
demo_key_secret_engineering="$(openssl rand -hex 24)"
demo_key_secret_sales="$(openssl rand -hex 24)"
demo_key_hash_support="$(printf '%s' "$demo_key_secret_support" | openssl dgst -sha256 -r | awk '{print $1}')"
demo_key_hash_engineering="$(printf '%s' "$demo_key_secret_engineering" | openssl dgst -sha256 -r | awk '{print $1}')"
demo_key_hash_sales="$(printf '%s' "$demo_key_secret_sales" | openssl dgst -sha256 -r | awk '{print $1}')"
demo_key_redacted_support="sk_gom_...${demo_key_secret_support: -4}"
demo_key_redacted_engineering="sk_gom_...${demo_key_secret_engineering: -4}"
demo_key_redacted_sales="sk_gom_...${demo_key_secret_sales: -4}"

# A compact spoken fixture saying "Prompt caching is active." Keeping it
# separate from the SQL makes every generated STT upload and TTS response
# playable without requiring provider credentials during seeding.
demo_audio_mp3_base64="$(tr -d '\r\n' < "$script_dir/fixtures/demo-prompt-caching.mp3.base64")"
demo_audio_mp3_bytes="$(printf '%s' "$demo_audio_mp3_base64" | openssl base64 -d -A | wc -c | awk '{print $1}')"

# Real provider types and names with a short, current selection of the models
# each provider lists, so the Models page stays readable and shows catalog
# pricing. Keys are placeholders;
# make demo sets CONFIGURED_PROVIDER_MODELS_MODE=allowlist so the gateway
# serves these lists without calling the upstream /models endpoints.
demo_providers_json="$(tr -d '\r\n' < "$script_dir/fixtures/demo-providers.json")"
demo_providers_json="${demo_providers_json//\'/\'\'}"

# Guardrail instances (plugin configurations) referenced by the seeded
# workflows below. Each config is written exactly as the matching built-in
# plugin parses it: the plugins reject unknown keys, so the demo doubles as a
# worked example of every built-in type. Single-quoted here so regular
# expression backslashes and $1 capture groups survive into the SQL heredoc.
guardrail_config_pii='{"rules":"[\\w.%+-]+@[\\w.-]+\\.[A-Za-z]{2,} => [redacted-email]\n\\+?[0-9][0-9 ().-]{9,}[0-9] => [redacted-phone]\nsk-[A-Za-z0-9]{20,} => [redacted-key]\n([0-9]{3})-([0-9]{2})-[0-9]{4} => $1-$2-XXXX","mode":"regex","case_insensitive":true,"roles":["user","assistant","tool"],"on_match":"replace"}'
guardrail_config_blocked_terms='{"rules":"(project|codename)[ -]nightingale => \nunreleased pricing sheet => ","mode":"regex","case_insensitive":true,"roles":["user"],"on_match":"block","message":"This request mentions material that may not leave the tenant. Remove it and try again.","block_status":403}'
guardrail_config_sales_tone='{"mode":"decorator","content":"Answer as a concise sales engineer. Never invent pricing, quote only figures present in the context, and end with one clear next step."}'
guardrail_config_headers='{"response_add":"X-GoModel-Demo: true","upstream_set":"X-Tenant: gomodel-demo","response_remove":"X-Powered-By"}'
guardrail_config_injection_judge='{"model":"openai/gpt-6-luna","target":"last_user","action":"block","message":"This prompt was rejected as a prompt-injection attempt.","block_status":400,"on_unclear":"warn","max_tokens":128,"temperature":0}'
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
workflow_payload_support='{"schema_version":2,"features":{"cache":true,"audit":true,"usage":true,"budget":true,"guardrails":true,"failover":true},"steps":[{"ref":"@@prompt-normalizer","phase":"prompt","step":0},{"ref":"@@blocked-terms","phase":"prompt","step":1}]}'
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
workflow_payload_support="$(expand_refs "$workflow_payload_support")"
workflow_payload_batch="$(expand_refs "$workflow_payload_batch")"
workflow_payload_anthropic="$(expand_refs "$workflow_payload_anthropic")"

workflow_hash_baseline_v1="$(payload_hash "$workflow_payload_baseline_v1")"
workflow_hash_baseline="$(payload_hash "$workflow_payload_baseline")"
workflow_hash_sales="$(payload_hash "$workflow_payload_sales")"
workflow_hash_support="$(payload_hash "$workflow_payload_support")"
workflow_hash_batch="$(payload_hash "$workflow_payload_batch")"
workflow_hash_anthropic="$(payload_hash "$workflow_payload_anthropic")"

# The period of the one budget seeded as exceeded: the month, unless it has
# only just begun, when so little was spent that the amount would look absurd;
# then the week.
if (( 10#$(date -u +%d) >= 4 )); then
  exceeded_budget_period=2592000
else
  exceeded_budget_period=604800
fi

mkdir -p "$(dirname "$db_path")"

sqlite3 "$db_path" "PRAGMA journal_mode = WAL;" >/dev/null

# Add columns introduced after the original demo seeder. Errors on fresh or
# already-migrated databases are benign; the schema below handles fresh files.
sqlite3 "$db_path" "ALTER TABLE usage ADD COLUMN labels JSON;" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE usage ADD COLUMN rewrite_tokens_saved INTEGER NOT NULL DEFAULT 0;" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE usage ADD COLUMN rewrite_cost_saved REAL;" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE usage ADD COLUMN session_id TEXT;" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE usage ADD COLUMN auth_key_id TEXT;" 2>/dev/null || true
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
sqlite3 "$db_path" "ALTER TABLE provider_credentials ADD COLUMN session_sticky_keys INTEGER NOT NULL DEFAULT TRUE;" 2>/dev/null || true
sqlite3 "$db_path" "ALTER TABLE provider_credentials ADD COLUMN proxy_url TEXT NOT NULL DEFAULT '';" 2>/dev/null || true

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
  auth_key_id TEXT,
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

CREATE TABLE IF NOT EXISTS provider_credentials (
  name TEXT PRIMARY KEY,
  type TEXT NOT NULL,
  api_keys TEXT NOT NULL DEFAULT '[]',
  session_sticky_keys INTEGER NOT NULL DEFAULT TRUE,
  base_url TEXT NOT NULL DEFAULT '',
  api_version TEXT NOT NULL DEFAULT '',
  backend TEXT NOT NULL DEFAULT '',
  auth_type TEXT NOT NULL DEFAULT '',
  api_mode TEXT NOT NULL DEFAULT '',
  vertex_project TEXT NOT NULL DEFAULT '',
  vertex_location TEXT NOT NULL DEFAULT '',
  service_account_file TEXT NOT NULL DEFAULT '',
  service_account_json TEXT NOT NULL DEFAULT '',
  service_account_json_base64 TEXT NOT NULL DEFAULT '',
  gcp_scope TEXT NOT NULL DEFAULT '',
  proxy_url TEXT NOT NULL DEFAULT '',
  models TEXT NOT NULL DEFAULT '[]',
  enabled INTEGER NOT NULL DEFAULT TRUE,
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
  -- The daily volume is meant to be read at a glance: quiet weekends, a
  -- steady growth trend across the window, and only a little noise on top.
  SELECT
    day_idx,
    day,
    CASE WHEN strftime('%w', day) IN ('0', '6') THEN 0.45 ELSE 1.0 END AS weekday_factor,
    0.75 + (day_idx * 0.50 / max(1, ${days} - 1)) AS trend_factor,
    0.94 + ((abs(random()) % 1200) / 10000.0) AS noise_factor
  FROM days
)
-- The count is always for a full day. Today's requests are spread over the
-- whole day like any other and the ones that have not happened yet are
-- dropped below, so a partly elapsed day follows the same daily curve.
SELECT
  day_idx,
  day,
  CAST(max(25, min(${max_requests}, round(
    ${avg_requests} * weekday_factor * trend_factor * noise_factor
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

-- Three teams, two or three members each: people and service accounts. Every
-- path has its own share of the traffic and its own model mix (demo_mix), so
-- the usage charts rank clearly instead of showing ten near-identical bars.
DROP TABLE IF EXISTS temp.demo_paths;
CREATE TEMP TABLE demo_paths(min_bucket INTEGER, max_bucket INTEGER, user_path TEXT);
INSERT INTO demo_paths VALUES
  (0, 2600, '/support/helpdesk-bot'),
  (2600, 4800, '/engineering/alice'),
  (4800, 6600, '/engineering/ci-bot'),
  (6600, 8200, '/sales/john'),
  (8200, 9200, '/engineering/evals'),
  (9200, 10000, '/support/maria');

-- Share of a day's requests per UTC hour: a working-day curve over a low
-- overnight floor of automated traffic.
DROP TABLE IF EXISTS temp.demo_hours;
CREATE TEMP TABLE demo_hours AS
WITH weights(hour, weight) AS (
  VALUES
    (0, 18), (1, 14), (2, 12), (3, 12), (4, 14), (5, 20), (6, 32), (7, 50),
    (8, 72), (9, 88), (10, 96), (11, 100), (12, 92), (13, 96), (14, 100), (15, 96),
    (16, 86), (17, 72), (18, 58), (19, 48), (20, 40), (21, 34), (22, 28), (23, 22)
),
cumulative AS (
  SELECT hour,
    sum(weight) OVER (ORDER BY hour) - weight AS before,
    sum(weight) OVER (ORDER BY hour) AS upto,
    sum(weight) OVER () AS total
  FROM weights
)
SELECT hour, before * 10000 / total AS min_bucket, upto * 10000 / total AS max_bucket
FROM cumulative;

DROP TABLE IF EXISTS temp.demo_templates;
CREATE TEMP TABLE demo_templates(
  label TEXT PRIMARY KEY,
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
-- Prices are the catalog's, in USD per million tokens.
INSERT INTO demo_templates VALUES
  ('chat-openai',  '/v1/chat/completions',     'openai',    'openai',    'gpt-6-luna',              9000, 27000, 1500, 5400, 0.100,  0.500, 1, 1),
  ('chat-groq',    '/v1/chat/completions',     'groq',      'groq',      'llama-3.1-8b-instant',    7000, 21000, 1200, 4200, 0.050,  0.080, 1, 1),
  ('chat-gemini',  '/v1/chat/completions',     'gemini',    'gemini',    'gemini-3.5-flash-lite',   9000, 27000, 1300, 4800, 0.300,  2.500, 1, 1),
  ('chat-bailian', '/v1/chat/completions',     'bailian',   'bailian',   'qwen3.8-flash',           8000, 25000, 1300, 4800, 0.150,  0.470, 1, 1),
  ('responses',    '/v1/responses',            'openai',    'openai',    'gpt-6.1-sol',            13000, 31000, 2100, 6600, 2.000, 10.000, 1, 1),
  ('messages',     '/v1/messages',             'anthropic', 'anthropic', 'claude-sonnet-5-5',      12000, 36000, 1800, 7200, 2.000, 10.000, 1, 1),
  ('embeddings',   '/v1/embeddings',           'openai',    'openai',    'text-embedding-3-small',  2400,  9600,    0,    1, 0.020,  0.000, 0, 0),
  ('stt',          '/v1/audio/transcriptions', 'openai',    'openai',    'gpt-4o-transcribe',       4800, 14400,  480, 1900, 2.500, 10.000, 0, 0),
  ('tts',          '/v1/audio/speech',         'openai',    'openai',    'gpt-4o-mini-tts',          900,  3600,  600, 2400, 0.600, 12.000, 0, 0);

-- What each path sends, in percent. A path with a model allowlist (see the
-- users table below) draws only from templates its policy permits, so the
-- generated history never shows a request the gateway would have rejected.
DROP TABLE IF EXISTS temp.demo_mix;
CREATE TEMP TABLE demo_mix AS
WITH weights(user_path, label, weight) AS (
  VALUES
    -- A chatbot with a voice channel, on the fast low-cost models.
    ('/support/helpdesk-bot', 'chat-gemini', 40),
    ('/support/helpdesk-bot', 'chat-groq', 25),
    ('/support/helpdesk-bot', 'chat-openai', 15),
    ('/support/helpdesk-bot', 'stt', 10),
    ('/support/helpdesk-bot', 'tts', 10),
    -- An engineer's coding assistant, mostly on Claude.
    ('/engineering/alice', 'messages', 45),
    ('/engineering/alice', 'chat-openai', 25),
    ('/engineering/alice', 'responses', 20),
    ('/engineering/alice', 'embeddings', 10),
    -- Automated jobs pinned to the cheapest models.
    ('/engineering/ci-bot', 'chat-groq', 45),
    ('/engineering/ci-bot', 'chat-bailian', 35),
    ('/engineering/ci-bot', 'embeddings', 20),
    -- Sales stays on OpenAI: drafting, call transcripts, account search.
    ('/sales/john', 'chat-openai', 45),
    ('/sales/john', 'responses', 25),
    ('/sales/john', 'stt', 15),
    ('/sales/john', 'embeddings', 10),
    ('/sales/john', 'tts', 5),
    -- The evaluation harness compares every chat model.
    ('/engineering/evals', 'chat-openai', 20),
    ('/engineering/evals', 'chat-groq', 20),
    ('/engineering/evals', 'chat-gemini', 20),
    ('/engineering/evals', 'chat-bailian', 20),
    ('/engineering/evals', 'messages', 20),
    -- A support agent's assistant.
    ('/support/maria', 'chat-openai', 60),
    ('/support/maria', 'chat-gemini', 30),
    ('/support/maria', 'embeddings', 10)
),
numbered AS (
  SELECT *, row_number() OVER () AS rn FROM weights
)
SELECT
  user_path,
  label,
  (sum(weight) OVER (PARTITION BY user_path ORDER BY rn) - weight) * 100 AS min_bucket,
  sum(weight) OVER (PARTITION BY user_path ORDER BY rn) * 100 AS max_bucket
FROM numbered;

-- A few realistic exchanges per path, so an opened request reads like the
-- work that path does.
DROP TABLE IF EXISTS temp.demo_prompts;
CREATE TEMP TABLE demo_prompts(prompt_path TEXT, variant INTEGER, system_text TEXT, user_text TEXT, reply_text TEXT);
INSERT INTO demo_prompts VALUES
  ('/support/helpdesk-bot', 0, 'You are the help-center assistant. Answer from the knowledge base and keep replies short.',
    'I was charged twice for my subscription this month. Can you check what happened?',
    'I can see two charges on the same day; the second one is a duplicate. I have opened a refund for it, and it should reach your account within five business days.'),
  ('/support/helpdesk-bot', 1, 'You are the help-center assistant. Answer from the knowledge base and keep replies short.',
    'How do I move my workspace to another region without losing the audit history?',
    'Open Settings, choose Region, and start a migration. The audit history moves with the workspace, and the old region stays readable for 30 days.'),
  ('/support/helpdesk-bot', 2, 'You are the help-center assistant. Answer from the knowledge base and keep replies short.',
    'The mobile app logs me out every time I switch networks. Is that expected?',
    'It is not expected. Update to the latest version, which keeps the session across network changes, and tell me if it still happens.'),
  ('/engineering/alice', 0, 'You are a senior Go reviewer. Point out bugs first, then style.',
    'Review this retry helper: it sleeps with a fixed one-second delay and retries on every error.',
    'Two problems: it retries errors that can never succeed, such as a 400, and the fixed delay makes all clients retry in step. Retry only on 429 and 5xx, and add exponential backoff with jitter.'),
  ('/engineering/alice', 1, 'You are a senior Go reviewer. Point out bugs first, then style.',
    'Why does this test pass on its own and fail when the whole package runs?',
    'The test reads a package-level map that another test changes. Give each test its own copy, or build the map in a helper, and the order stops mattering.'),
  ('/engineering/alice', 2, 'You are a senior Go reviewer. Point out bugs first, then style.',
    'Write a table-driven test for a function that parses durations such as 90s, 5m, and 1h30m.',
    'Here is a table with the valid forms, an empty string, a missing unit, and a negative value, each with the expected duration or error.'),
  ('/engineering/ci-bot', 0, 'You label build failures. Reply with one category and one sentence.',
    'Classify this failure: "dial tcp 10.0.4.17:5432: connect: connection refused" in the integration job.',
    'infrastructure: the database container was not ready when the job connected.'),
  ('/engineering/ci-bot', 1, 'You label build failures. Reply with one category and one sentence.',
    'Classify this failure: "expected 200, got 502" in test_checkout_flow, three runs out of ten.',
    'flaky-test: the upstream stub answers late under load, so the request times out intermittently.'),
  ('/engineering/ci-bot', 2, 'You write release notes from merged pull request titles.',
    'Summarize these 14 merged pull requests for the weekly changelog.',
    'Highlights: faster provider saves, a clearer empty state on the overview charts, and three fixes to budget resets.'),
  ('/sales/john', 0, 'You help account executives write clear, honest follow-ups.',
    'Draft a follow-up to a prospect who asked about SSO and a 99.9% uptime commitment.',
    'Thanks for the call today. SSO is included in the plan we discussed, and the uptime commitment is part of the standard agreement. Shall I send the security overview before Thursday?'),
  ('/sales/john', 1, 'You help account executives write clear, honest follow-ups.',
    'Summarize this discovery call: 40 engineers, two providers today, no cost visibility per team.',
    'A 40-person engineering team on two providers with no per-team cost view. They want budgets per team and one place to audit requests. Next step: a pilot with the platform team.'),
  ('/sales/john', 2, 'You help account executives write clear, honest follow-ups.',
    'Which of my open opportunities have had no activity for two weeks?',
    'Three have been quiet for two weeks or more. The largest is the renewal that is waiting on a security review.'),
  ('/engineering/evals', 0, 'Answer the question. Reply with the answer only.',
    'Evaluation item 118: which HTTP status means the client sent too many requests?',
    '429'),
  ('/engineering/evals', 1, 'Answer the question. Reply with the answer only.',
    'Evaluation item 245: extract the invoice total from "Subtotal 1,180.00, tax 94.40, total 1,274.40".',
    '1,274.40'),
  ('/engineering/evals', 2, 'Answer the question. Reply with the answer only.',
    'Evaluation item 302: translate "the request was rate limited" into German.',
    'Die Anfrage wurde ratenbegrenzt.'),
  ('/support/maria', 0, 'You help a support agent reply to customers. Suggest a reply the agent can edit.',
    'Suggest a reply: the customer asks for an invoice with their VAT number added.',
    'Happy to help. I have added your VAT number to the account, and the corrected invoice is attached. Future invoices will include it automatically.'),
  ('/support/maria', 1, 'You help a support agent reply to customers. Suggest a reply the agent can edit.',
    'Summarize this ticket thread of 23 messages for the handover to the next shift.',
    'The customer cannot export reports larger than 50 MB. Engineering confirmed a limit in the export job, and a fix ships Tuesday. The customer has a workaround and expects an update then.'),
  ('/support/maria', 2, 'You help a support agent reply to customers. Suggest a reply the agent can edit.',
    'Is a refund allowed 45 days after purchase under the current policy?',
    'The policy allows refunds within 30 days. After that it needs a team lead to approve an exception, which is usually granted for billing errors.');

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
  abs(random()) % 10000 AS hour_bucket,
  abs(random()) % 3600 AS second_of_hour,
  abs(random()) AS token_noise
FROM demo_days d
JOIN demo_slots s ON s.slot_idx < d.request_count;

DROP TABLE IF EXISTS temp.demo_generated;
CREATE TEMP TABLE demo_generated AS
WITH timed AS (
  SELECT
    b.*,
    h.hour * 3600 + b.second_of_hour AS second_of_day
  FROM demo_random b
  JOIN demo_hours h ON b.hour_bucket >= h.min_bucket AND b.hour_bucket < h.max_bucket
),
pathed AS (
  SELECT
    b.*,
    p.user_path,
    p.min_bucket AS path_min
  FROM timed b
  JOIN demo_paths p ON b.path_bucket >= p.min_bucket AND b.path_bucket < p.max_bucket
  -- Today's requests that have not happened yet.
  WHERE b.day != date(${seed_utc_epoch}, 'unixepoch') OR b.second_of_day <= ${current_utc_second}
),
chosen AS (
  SELECT
    b.*,
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
      WHEN b.user_path GLOB '/support/helpdesk-bot*' THEN '${prefix}-wf-support'
      WHEN b.user_path GLOB '/engineering/ci-bot*' THEN '${prefix}-wf-batch'
      WHEN b.user_path GLOB '/engineering/evals*' THEN '${prefix}-wf-evals'
      WHEN t.provider = 'anthropic' THEN '${prefix}-wf-anthropic'
      ELSE '${prefix}-wf-global-v2'
    END AS workflow_version_id,
    CASE WHEN b.user_path GLOB '/engineering/evals*' THEN 0 ELSE 1 END AS workflow_guardrails
  FROM pathed b
  JOIN demo_mix x ON x.user_path = b.user_path
    AND b.template_bucket >= x.min_bucket AND b.template_bucket < x.max_bucket
  JOIN demo_templates t ON t.label = x.label
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
-- Conversational requests in the same three-hour window on the same day, user
-- path, and endpoint share a session key. Grouping by endpoint (rather than
-- provider/model) lets a conversation retain its thread when a virtual model
-- routes a later turn elsewhere. The key stays below 2^31 so the hex mixing
-- below cannot overflow SQLite's 64-bit integer arithmetic.
session_keys AS (
  SELECT
    *,
    (((day_idx * 11 + (second_of_day / 10800)) * 131071 + path_min) * 8191 +
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
-- guardrail or a budget stopped are logged without a usage row, the way the
-- gateway records a request that produced no usage.
outcomes AS (
  SELECT
    *,
    CASE
      WHEN cache_type IS NOT NULL THEN 'ok'
      WHEN workflow_version_id = '${prefix}-wf-support' AND guardrail_bucket < 120 THEN 'guardrail_blocked'
      WHEN workflow_version_id = '${prefix}-wf-anthropic' AND guardrail_bucket < 180 THEN 'guardrail_blocked'
      -- The help-desk bot runs out of its budget now and then: right now, so
      -- its latest requests are the refused ones, and, while its budget is
      -- the monthly one, on the last afternoon of a month.
      WHEN user_path = '/support/helpdesk-bot' AND (
        (day = date(${seed_utc_epoch}, 'unixepoch') AND second_of_day >= (${current_utc_second} + 1) * 9 / 10)
        OR (${exceeded_budget_period} = 2592000 AND strftime('%d', day, '+1 day') = '01' AND second_of_day >= 43200)
      )
      THEN 'budget_blocked'
      -- One provider incident in the window: Groq fails most requests for an
      -- afternoon on a Tuesday one to two weeks back, so the status chart
      -- shows one clear spike and the request log a run of failovers.
      WHEN provider = 'groq' AND day = date(${seed_utc_epoch}, 'unixepoch', '-13 days', 'weekday 2')
        AND second_of_day >= 43200 AND second_of_day < 61200
        AND outcome_bucket < 7000
      THEN 'provider_error'
      WHEN abs(token_noise / 131) % 1000 >= 997 THEN 'provider_error'
      WHEN abs(token_noise / 131) % 1000 >= 992 THEN 'rate_limited'
      ELSE 'ok'
    END AS request_outcome
  FROM session_turns
)
SELECT
  *,
  CASE WHEN request_outcome IN ('budget_blocked', 'guardrail_blocked') THEN 0 ELSE 1 END AS usage_recorded,
  -- The period of the budget that stopped a request (see demo_budgets).
  CASE
    WHEN day = date(${seed_utc_epoch}, 'unixepoch') AND ${exceeded_budget_period} = 604800 THEN 'weekly'
    ELSE 'monthly'
  END AS budget_period,
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
    WHEN workflow_version_id IN ('${prefix}-wf-support', '${prefix}-wf-batch')
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
  -- stay consistent; 'smart' is scoped to engineering.
  CASE
    WHEN label NOT IN ('chat-openai', 'chat-groq', 'chat-gemini', 'chat-bailian', 'responses', 'messages') THEN NULL
    WHEN alias_bucket >= 2200 THEN NULL
    WHEN provider = 'anthropic' THEN CASE WHEN user_path GLOB '/engineering*' THEN 'smart' ELSE NULL END
    WHEN label = 'responses' THEN CASE WHEN user_path GLOB '/engineering*' THEN 'smart' ELSE NULL END
    WHEN provider = 'openai' THEN CASE alias_bucket % 2
      WHEN 0 THEN 'cheap'
      ELSE 'resilient'
    END
    ELSE CASE alias_bucket % 3
      WHEN 0 THEN 'fast'
      WHEN 1 THEN 'cheap'
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
  input_cost, output_cost, total_cost, cost_source, costs_calculation_caveat, auth_key_id
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
  -- Request labels as extracted from tagging headers: most traffic carries
  -- its environment, a little carries two labels, and the rest is unlabelled
  -- (NULL).
  CASE
    WHEN label_bucket < 5000 THEN json_array('env:prod')
    WHEN label_bucket < 6400 THEN json_array('env:staging')
    WHEN label_bucket < 7200 THEN json_array('experiment:rag-v2')
    WHEN label_bucket < 7800 THEN json_array('env:prod', 'priority:high')
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
  '',
  -- Same key attribution as the request log rows below.
  CASE
    WHEN user_path GLOB '/support*' THEN '${prefix}-key-support'
    WHEN user_path IN ('/engineering/alice', '/engineering/ci-bot') THEN '${prefix}-key-engineering'
    WHEN user_path GLOB '/sales/john*' THEN '${prefix}-key-sales'
    ELSE NULL
  END
-- Requests stopped by a budget or a prompt guardrail never reach a provider,
-- so they are logged without a usage row.
FROM demo_generated
WHERE usage_recorded = 1;

-- The request-revision chain, one row per step that changed the
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
      'input', 'Earlier turns condensed to the facts the next answer needs.',
      'metadata', json_object('demo', json('true'), 'rewritten', json('true'))
    ),
    'detail', json_object(
      'strategy', 'context-compression',
      'tokens_saved_estimate', rewrite_tokens_saved,
      'preserved_sections', json_array('facts', 'identifiers', 'open questions')
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
        'content', 'Turn ' || session_turn || ' restated as one self-contained question, with every identifier it refers to kept.'
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
    'rewriter', CASE WHEN workflow_version_id = '${prefix}-wf-support' THEN '${prefix}-blocked-terms' ELSE '${prefix}-prompt-injection-judge' END,
    'bytes_before', bytes_normalized,
    'bytes_after', bytes_normalized,
    'no_change', json('true'),
    'detail', json_object(
      'phase', 'prompt',
      'action', 'block',
      'code', 'guardrail_blocked',
      'message', CASE
        WHEN workflow_version_id = '${prefix}-wf-support'
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
  -- Requests stopped early are fast, and a cache hit skips the provider.
  CASE
    WHEN request_outcome = 'budget_blocked' THEN 900000 + (token_noise % 2200000)
    WHEN request_outcome = 'guardrail_blocked' AND workflow_version_id = '${prefix}-wf-support'
      THEN 1400000 + (token_noise % 3600000)
    WHEN request_outcome = 'guardrail_blocked' THEN 190000000 + (token_noise % 520000000)
    WHEN cache_type IS NOT NULL THEN 8000000 + (token_noise % 12000000)
    ELSE CAST((90000000 + (token_noise % 260000000)) * CASE provider
      WHEN 'groq' THEN 0.45
      WHEN 'gemini' THEN 0.8
      WHEN 'anthropic' THEN 1.6
      WHEN 'bailian' THEN 2.2
      ELSE 1.0
    END AS INTEGER)
  END
  -- The prompt phase runs first, so a guardrail that calls a model is part of
  -- the total whatever the request did next: a cache hit skipped the provider,
  -- and a later step blocked the request only after this call had been paid
  -- for.
  + CASE WHEN guardrail_normalized = 1 THEN 210000000 + (token_noise % 260000000) ELSE 0 END
  + CASE WHEN guardrail_warned = 1 THEN 170000000 + (token_noise % 240000000) ELSE 0 END,
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
      CASE WHEN workflow_version_id = '${prefix}-wf-support' THEN 403 ELSE 400 END
    WHEN 'rate_limited' THEN 429
    WHEN 'provider_error' THEN 500
    ELSE 200
  END,
  request_id,
  -- The generated demo keys own the traffic of their user paths. The
  -- evaluation harness has no key of its own and runs on the master key.
  CASE
    WHEN user_path GLOB '/support*' THEN '${prefix}-key-support'
    WHEN user_path IN ('/engineering/alice', '/engineering/ci-bot') THEN '${prefix}-key-engineering'
    WHEN user_path GLOB '/sales/john*' THEN '${prefix}-key-sales'
    ELSE NULL
  END,
  CASE
    WHEN user_path = '/engineering/evals' THEN 'master_key'
    ELSE 'api_key'
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
      WHEN 'budget_blocked' THEN 'budget exceeded for ' || user_path || ' ' || budget_period || ' limit'
      WHEN 'guardrail_blocked' THEN
        CASE
          WHEN workflow_version_id = '${prefix}-wf-support'
            THEN 'This request mentions material that may not leave the tenant. Remove it and try again.'
          ELSE 'This prompt was rejected as a prompt-injection attempt.'
        END
      WHEN 'rate_limited' THEN 'Synthetic rate limit for demo request log inspection. Retry after a moment.'
      WHEN 'provider_error' THEN 'Synthetic upstream provider error for demo request log inspection.'
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
        WHEN 'groq' THEN 'gemini/gemini-3.5-flash-lite'
        WHEN 'gemini' THEN 'groq/llama-3.1-8b-instant'
        WHEN 'bailian' THEN 'groq/llama-3.1-8b-instant'
        ELSE 'openai/gpt-6-luna'
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
          json_object('role', 'system', 'content', system_text),
          json_object('role', 'user', 'content', user_text)
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
          json_object('role', 'user', 'content', user_text)
        ),
        'instructions', system_text,
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
        'system', system_text,
        'messages', json_array(
          json_object('role', 'user', 'content', json_array(
            json_object('type', 'text', 'text', user_text)
          ))
        ),
        'max_tokens', output_tokens,
        'stream', CASE WHEN slot_idx % 5 = 0 THEN json('true') ELSE json('false') END,
        'temperature', round(0.10 + ((token_noise % 55) / 100.0), 2)
      )
      WHEN label = 'embeddings' THEN json_object(
        'model', provider_name || '/' || model,
        'input', json_array(user_text, reply_text),
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
          'message', 'budget exceeded for ' || user_path || ' ' || budget_period || ' limit',
          'type', 'rate_limit_error',
          'code', 'budget_exceeded',
          'param', NULL
        )
      )
      WHEN request_outcome = 'guardrail_blocked' THEN json_object(
        'error', json_object(
          'message', CASE
            WHEN workflow_version_id = '${prefix}-wf-support'
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
          'message', 'Synthetic upstream provider error for demo request log inspection.',
          'type', 'provider_error',
          'code', 'demo_provider_error',
          'param', 'model'
        )
      )
      WHEN request_outcome = 'rate_limited' THEN json_object(
        'error', json_object(
          'message', 'Synthetic rate limit for demo request log inspection. Retry after a moment.',
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
            'content', reply_text
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
            'text', reply_text
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
          'text', reply_text
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
          json_object('object', 'embedding', 'index', 1, 'embedding', json_array(-0.021, 0.045, 0.016, -0.008))
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
FROM demo_generated
JOIN demo_prompts ON prompt_path = user_path AND variant = (session_key + session_turn) % 3
LEFT JOIN demo_revision_arrays USING (audit_id);

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
    WHEN 'groq' THEN 'gemini-3.5-flash-lite'
    WHEN 'gemini' THEN 'llama-3.1-8b-instant'
    WHEN 'bailian' THEN 'llama-3.1-8b-instant'
    ELSE 'gpt-6-luna'
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
    '${prefix}: rewrites help-desk and batch prompts into self-contained questions',
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
    'global', 'path:/sales', 'path:/support/helpdesk-bot',
    'path:/engineering/ci-bot', 'path:/engineering/evals', 'provider:anthropic'
  );

INSERT INTO workflow_versions (
  id, scope_provider, scope_model, scope_user_path, scope_key, version,
  active, managed_default, name, description, workflow_payload, workflow_hash, created_at
)
SELECT '${prefix}-wf-global-v1', NULL, NULL, NULL, 'global',
  (SELECT coalesce(max(version), 0) FROM workflow_versions WHERE scope_key = 'global') + 1,
  FALSE, FALSE, 'Gateway baseline',
  'Superseded: cache, request logging, usage, budgets, and failover without guardrails',
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
SELECT '${prefix}-wf-support', NULL, NULL, '/support/helpdesk-bot', 'path:/support/helpdesk-bot',
  (SELECT coalesce(max(version), 0) FROM workflow_versions WHERE scope_key = 'path:/support/helpdesk-bot') + 1,
  TRUE, FALSE, 'Help-desk bot',
  'Normalizes customer messages and rejects confidential program names',
  '${workflow_payload_support}', '${workflow_hash_support}', ${seed_utc_epoch} - 2160000;

INSERT INTO workflow_versions (
  id, scope_provider, scope_model, scope_user_path, scope_key, version,
  active, managed_default, name, description, workflow_payload, workflow_hash, created_at
)
SELECT '${prefix}-wf-batch', NULL, NULL, '/engineering/ci-bot', 'path:/engineering/ci-bot',
  (SELECT coalesce(max(version), 0) FROM workflow_versions WHERE scope_key = 'path:/engineering/ci-bot') + 1,
  TRUE, FALSE, 'Batch jobs',
  'Prompt normalization and redaction for batch traffic, failover off',
  '${workflow_payload_batch}', '${workflow_hash_batch}', ${seed_utc_epoch} - 1296000;

INSERT INTO workflow_versions (
  id, scope_provider, scope_model, scope_user_path, scope_key, version,
  active, managed_default, name, description, workflow_payload, workflow_hash, created_at
)
SELECT '${prefix}-wf-evals', NULL, NULL, '/engineering/evals', 'path:/engineering/evals',
  (SELECT coalesce(max(version), 0) FROM workflow_versions WHERE scope_key = 'path:/engineering/evals') + 1,
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

-- A handful of budgets, one per state worth seeing: comfortable, filling up,
-- close to the limit, and exceeded, across the daily, weekly, and monthly
-- periods and both scopes (user path and request label).
--
-- Amounts follow the generated spend, so the page shows these states whenever
-- the seed runs. A budget is sized so that what its subject spent in the
-- current period is used_pct of it, but never below typical_factor times what
-- the subject spends in a typical period: early in a period the bar is then
-- honestly short instead of the amount being absurdly small. The help-desk
-- bot's budget is the exceeded one (no floor), matching the requests the
-- generator stopped with budget_exceeded. last_reset_at stays NULL so a
-- budget counts its whole current period, not just the time since the seed.
DROP TABLE IF EXISTS temp.demo_budgets;
CREATE TEMP TABLE demo_budgets(
  scope TEXT, subject TEXT, period_seconds INTEGER, used_pct REAL, typical_factor REAL
);
INSERT INTO demo_budgets VALUES
  ('user_path', '/', 2592000, 44, 0.9),
  ('user_path', '/engineering', 2592000, 61, 0.9),
  ('user_path', '/engineering/alice', 86400, 80, 1.25),
  ('user_path', '/support', 604800, 37, 0.9),
  ('user_path', '/support/helpdesk-bot', ${exceeded_budget_period}, 103, 0),
  ('user_path', '/sales', 604800, 72, 0.9),
  ('label', 'env:prod', 2592000, 52, 0.9),
  ('label', 'experiment:rag-v2', 604800, 88, 0.9);

-- Spend per budget subject and day; a usage row counts once for every budget
-- whose subject covers it.
DROP TABLE IF EXISTS temp.demo_budget_spend;
CREATE TEMP TABLE demo_budget_spend AS
SELECT b.scope, b.subject, b.period_seconds, substr(u.timestamp, 1, 10) AS day, sum(u.total_cost) AS cost
FROM demo_budgets b
JOIN usage u ON u.id GLOB '${prefix}-*'
  AND CASE b.scope
    WHEN 'label' THEN EXISTS (SELECT 1 FROM json_each(u.labels) WHERE value = b.subject)
    ELSE b.subject = '/' OR u.user_path = b.subject OR u.user_path GLOB b.subject || '/*'
  END
GROUP BY 1, 2, 3, 4;

WITH periods AS (
  SELECT
    b.*,
    CASE b.period_seconds
      WHEN 86400 THEN date(${seed_utc_epoch}, 'unixepoch')
      -- Weeks reset on Monday and months on the 1st (see budget_settings).
      WHEN 604800 THEN date(${seed_utc_epoch}, 'unixepoch', '-6 days', 'weekday 1')
      ELSE date(${seed_utc_epoch}, 'unixepoch', 'start of month')
    END AS period_start,
    b.period_seconds / 86400 AS period_days
  FROM demo_budgets b
),
spent AS (
  SELECT
    p.*,
    (
      SELECT coalesce(sum(cost), 0) FROM demo_budget_spend s
      WHERE s.scope = p.scope AND s.subject = p.subject AND s.period_seconds = p.period_seconds
        AND s.day >= p.period_start
    ) AS period_spend,
    -- A typical day is the average of the complete days seeded.
    (
      SELECT coalesce(sum(cost), 0) / max(1, ${days} - 1) FROM demo_budget_spend s
      WHERE s.scope = p.scope AND s.subject = p.subject AND s.period_seconds = p.period_seconds
        AND s.day < date(${seed_utc_epoch}, 'unixepoch')
    ) AS typical_day
  FROM periods p
)
INSERT INTO budgets (scope, subject, per_child, period_seconds, amount, source, last_reset_at, created_at, updated_at)
SELECT
  scope,
  subject,
  0,
  period_seconds,
  max(0.01, round(max(
    period_spend * 100.0 / used_pct,
    -- Nothing spent in the current period (a seed that ends in the past):
    -- every budget falls back to its typical period.
    typical_day * period_days * CASE WHEN period_spend > 0 THEN typical_factor ELSE 0.9 END
  ), 2)),
  '${prefix}',
  NULL,
  ${seed_utc_epoch},
  ${seed_utc_epoch}
FROM spent
WHERE true
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
-- One rule of each kind: per-minute, hourly, and daily windows, a concurrency
-- cap (period 0), and the three scopes.
INSERT INTO demo_rate_limits VALUES
  ('user_path', '/support/helpdesk-bot', 60, 600, 2000000),
  ('user_path', '/engineering/ci-bot', 0, 8, NULL),
  ('user_path', '/sales', 86400, 5000, 60000000),
  ('provider', 'openai', 60, 1500, 30000000),
  ('provider', 'anthropic', 3600, 7500, 120000000),
  ('model', 'groq/llama-3.1-8b-instant', 60, 800, 16000000);

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
    '${prefix}-key-support',
    'Support',
    'Demo key for the help-desk bot and the support agents.',
    '/support',
    json_array('env:demo', 'team:support'),
    NULL,
    0,
    '${demo_key_redacted_support}',
    '${demo_key_hash_support}',
    1, NULL, NULL,
    strftime('%s', 'now'), strftime('%s', 'now')
  ),
  (
    '${prefix}-key-engineering',
    'Engineering',
    'Demo key for engineers and their automated jobs.',
    '/engineering',
    json_array('env:demo', 'team:engineering'),
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
    'Demo key for sales drafting and call summaries.',
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
-- traffic respects every list here (see demo_mix above).
INSERT OR IGNORE INTO users (user_path, allowed_models, description, created_at, updated_at)
VALUES
  (
    '/engineering',
    '[]',
    '${prefix}: engineering group node without model restrictions',
    strftime('%s', 'now'), strftime('%s', 'now')
  ),
  (
    '/engineering/ci-bot',
    json_array('groq/llama-3.1-8b-instant', 'qwen3.8-flash', 'openai/text-embedding-3-small'),
    '${prefix}: automated jobs are pinned to the cheapest models',
    strftime('%s', 'now'), strftime('%s', 'now')
  ),
  (
    '/support',
    json_array('openai/', 'gemini/', 'groq/'),
    '${prefix}: support stays on fast, low-cost providers',
    strftime('%s', 'now'), strftime('%s', 'now')
  ),
  (
    '/sales',
    json_array('openai/', 'anthropic/claude-sonnet-5-5'),
    '${prefix}: sales uses OpenAI plus Claude Sonnet for long summaries',
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

-- Named virtual models, one per routing idea: a weighted latency pool, a
-- lowest-cost pool, and a pool only one team may use. Existing operator-owned
-- aliases with these names win. Session affinity stays on its default (sticky)
-- except for 'cheap', which opts out so the editor's "Session keeping"
-- checkbox shows both states.
INSERT OR IGNORE INTO virtual_models (
  source, targets, strategy, session_affinity, provider_name, model, user_paths,
  description, enabled, created_at, updated_at
)
VALUES
  (
    'fast',
    json_array(
      json_object('provider', 'groq', 'model', 'llama-3.1-8b-instant', 'weight', 3),
      json_object('provider', 'gemini', 'model', 'gemini-3.5-flash-lite', 'weight', 2),
      json_object('provider', 'bailian', 'model', 'qwen3.8-flash', 'weight', 2)
    ),
    'round_robin', '', '', '', '[]',
    '${prefix}: latency-oriented weighted model pool',
    1, strftime('%s', 'now'), strftime('%s', 'now')
  ),
  (
    'cheap',
    json_array(
      json_object('provider', 'groq', 'model', 'llama-3.1-8b-instant', 'weight', 1),
      json_object('provider', 'gemini', 'model', 'gemini-3.5-flash-lite', 'weight', 1),
      json_object('provider', 'bailian', 'model', 'qwen3.8-flash', 'weight', 1),
      json_object('provider', 'openai', 'model', 'gpt-6-luna', 'weight', 1)
    ),
    'cost', 'false', '', '', '[]',
    '${prefix}: lowest-cost available target',
    1, strftime('%s', 'now'), strftime('%s', 'now')
  ),
  (
    'smart',
    json_array(
      json_object('provider', 'anthropic', 'model', 'claude-sonnet-5-5', 'weight', 2),
      json_object('provider', 'openai', 'model', 'gpt-6.1-sol', 'weight', 1)
    ),
    'round_robin', '', '', '', json_array('/engineering'),
    '${prefix}: quality-oriented pool scoped to engineering',
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
      json_object('provider', 'openai', 'model', 'gpt-6-luna'),
      json_object('provider', 'groq', 'model', 'llama-3.1-8b-instant'),
      json_object('provider', 'gemini', 'model', 'gemini-3.5-flash-lite'),
      json_object('provider', 'bailian', 'model', 'qwen3.8-flash')
    ),
    'failover', '', '', '', '[]',
    '${prefix}: priority-ordered failover pool',
    1, strftime('%s', 'now'), strftime('%s', 'now')
  );

-- Providers named after their real types, so the Models page prices each
-- model from the catalog. Bedrock takes no key: it signs with the host's AWS
-- credential chain, so its fixture endpoint is a .invalid host that never
-- resolves and demo traffic cannot run paid inference on a real account.
-- INSERT OR IGNORE keeps an operator's own provider
-- of the same name, and config/env providers shadow these rows anyway.
INSERT OR IGNORE INTO provider_credentials (
  name, type, api_keys, base_url, models, enabled, created_at, updated_at
)
SELECT
  json_extract(value, '$.name'),
  json_extract(value, '$.type'),
  CASE json_extract(value, '$.type')
    WHEN 'bedrock' THEN '[]'
    ELSE json_array('demo-placeholder-key')
  END,
  coalesce(json_extract(value, '$.base_url'), ''),
  json_extract(value, '$.models'),
  1, strftime('%s', 'now'), strftime('%s', 'now')
FROM json_each('${demo_providers_json}');

-- A reseed refreshes the model lists of the providers an earlier seed created,
-- recognised by the placeholder key (Bedrock: by its unresolvable endpoint).
UPDATE provider_credentials
SET models = fixture.models, updated_at = strftime('%s', 'now')
FROM (
  SELECT json_extract(value, '$.name') AS name, json_extract(value, '$.models') AS models
  FROM json_each('${demo_providers_json}')
) AS fixture
WHERE provider_credentials.name = fixture.name
  AND provider_credentials.models != fixture.models
  AND (provider_credentials.api_keys = json_array('demo-placeholder-key')
    OR provider_credentials.base_url LIKE '%.demo.invalid%');

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
SELECT 'provider_rows', name, type, json_array_length(models) FROM provider_credentials ORDER BY name;
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
  /support      sk_gom_${demo_key_secret_support}
  /engineering  sk_gom_${demo_key_secret_engineering}
  /sales/john   sk_gom_${demo_key_secret_sales}

Open the dashboard and use a recent 90-day date range. To replace this generated
dataset, rerun the script with the same DEMO_SEED_PREFIX. To keep multiple
datasets side by side, use a different DEMO_SEED_PREFIX.

Request log entries carry session ids, so the Requests page groups them into
threads by default ("Group by session"); failed and retried requests carry
attempt trails visible in the request drawer.

Every request resolves to one of the seeded workflows, so the request drawer
renders its pipeline. Guardrails cover all built-in plugin types and are
attached across the prompt and response phases; requests a prompt guardrail
blocked, and requests a budget stopped, are logged without a usage row, the
way the gateway records a request that never reached a provider. Start the
gateway with GUARDRAILS_ENABLED=true (make demo does) to see the Guardrails
and Plugins pages live; the two model-backed guardrails fail open, so a
gateway without those provider keys keeps serving traffic.

Traffic comes from three teams (/support, /engineering, /sales), each member
with its own model mix. The Users page shows their model allowlists
(/engineering/ci-bot narrows its group), and the Sales key carries a per-key
allowlist on top of the /sales policy. Budgets show one state each, from
comfortable to exceeded, across periods and both scopes; the help-desk bot's
budget is the exceeded one, and its latest requests are refused.

The Providers and Models pages list OpenAI, Anthropic, Bedrock, DeepSeek,
Gemini, Groq, Bailian, and xAI with a short selection of their models and
catalog prices.
Their keys are placeholders and Bedrock points at an unresolvable endpoint, so
requests to them fail upstream without spending anything; a provider of
the same name in config, env, or the store keeps its own settings.

Rate-limit counters are live process state and start at zero when GoModel starts.
Use the generated API keys to make requests and populate those counters.
EOF

package litellmmigrate

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvert_Resilience(t *testing.T) {
	cfg, result := convertYAML(t, `
model_list:
  - model_name: m
    litellm_params: {model: openai/gpt-4o}
router_settings:
  num_retries: 2
  timeout: 45.5
  allowed_fails: 3
  cooldown_time: 30
litellm_settings:
  num_retries: 9
  request_timeout: 999
`)
	require.NotNil(t, cfg.Resilience)
	require.NotNil(t, cfg.Resilience.Retry)
	assert.Equal(t, 2, *cfg.Resilience.Retry.MaxRetries, "router_settings wins over litellm_settings")
	assert.Equal(t, &httpOut{Timeout: 46}, cfg.HTTP)
	assert.Equal(t, &circuitBreakerOut{FailureThreshold: 4, Timeout: "30s"}, cfg.Resilience.CircuitBreaker)
	assert.Empty(t, findings(result, SeveritySkipped), "every key above is handled")
	assert.Contains(t, findings(result, SeverityInfo), "litellm_settings.num_retries: ignored: router_settings.num_retries takes precedence, as in LiteLLM")
}

func TestConvert_ZeroRetriesIsKept(t *testing.T) {
	cfg, _ := convertYAML(t, `
litellm_settings:
  num_retries: 0
`)
	require.NotNil(t, cfg.Resilience)
	require.NotNil(t, cfg.Resilience.Retry)
	assert.Equal(t, 0, *cfg.Resilience.Retry.MaxRetries)
}

func TestConvert_Callbacks(t *testing.T) {
	cfg, result := convertYAML(t, `
litellm_settings:
  callbacks: ["prometheus", "otel"]
  success_callback: ["langfuse", "datadog"]
`)
	assert.Equal(t, &enabledOut{Enabled: true}, cfg.Metrics)
	assert.Equal(t, &enabledOut{Enabled: true}, cfg.OpenTelemetry)
	assert.Contains(t, findings(result, SeverityWarning), "litellm_settings.callbacks: langfuse: send traces to Langfuse through GoModel's OpenTelemetry export; see /guides/langfuse")
	assert.Contains(t, findings(result, SeveritySkipped), "litellm_settings.callbacks: datadog: no GoModel integration; GoModel keeps request and usage logs in its own storage and exports OpenTelemetry")
}

func TestConvert_MasterKey(t *testing.T) {
	tests := []struct {
		name       string
		masterKey  string
		wantServer *serverOut
		wantEnv    string
	}{
		{name: "inline", masterKey: "sk-1234", wantEnv: "GOMODEL_MASTER_KEY=sk-1234"},
		{name: "environment", masterKey: "os.environ/LITELLM_MASTER_KEY", wantServer: &serverOut{MasterKey: "${LITELLM_MASTER_KEY}"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, result := convertYAML(t, "general_settings:\n  master_key: "+tt.masterKey+"\n")
			assert.Equal(t, tt.wantServer, cfg.Server)
			if tt.wantEnv != "" {
				assert.Contains(t, string(result.Env), tt.wantEnv)
				assert.NotContains(t, string(result.Config), "sk-1234")
			}
		})
	}
}

// GOMODEL_MASTER_KEY overrides server.master_key, so the LiteLLM master key
// must be the only value under that name.
func TestConvert_MasterKeyOwnsGoModelVariable(t *testing.T) {
	const moved = "environment_variables.GOMODEL_MASTER_KEY: moved to LITELLM_GOMODEL_MASTER_KEY in .env: GoModel uses GOMODEL_MASTER_KEY as its master key, so it would replace general_settings.master_key"
	tests := []struct {
		name        string
		litellm     string
		wantEnv     []string
		wantAPIKey  string
		wantServer  *serverOut
		wantWarning string
	}{
		{
			name: "inline key takes the name",
			litellm: `
environment_variables:
  GOMODEL_MASTER_KEY: something-else
general_settings:
  master_key: sk-1234
`,
			wantEnv:     []string{"LITELLM_GOMODEL_MASTER_KEY=something-else", "GOMODEL_MASTER_KEY=sk-1234"},
			wantWarning: moved,
		},
		{
			name: "referenced key frees the name",
			litellm: `
environment_variables:
  GOMODEL_MASTER_KEY: something-else
general_settings:
  master_key: os.environ/LITELLM_MASTER_KEY
`,
			wantEnv:     []string{"LITELLM_GOMODEL_MASTER_KEY=something-else"},
			wantServer:  &serverOut{MasterKey: "${LITELLM_MASTER_KEY}"},
			wantWarning: moved,
		},
		{
			name: "settings reading the moved value follow it",
			litellm: `
environment_variables:
  GOMODEL_MASTER_KEY: sk-provider
model_list:
  - model_name: m
    litellm_params: {model: openai/gpt-4o, api_key: os.environ/GOMODEL_MASTER_KEY}
general_settings:
  master_key: sk-1234
`,
			wantEnv:     []string{"LITELLM_GOMODEL_MASTER_KEY=sk-provider", "GOMODEL_MASTER_KEY=sk-1234"},
			wantAPIKey:  "${LITELLM_GOMODEL_MASTER_KEY}",
			wantWarning: moved,
		},
		{
			name: "same value is kept",
			litellm: `
environment_variables:
  GOMODEL_MASTER_KEY: sk-1234
general_settings:
  master_key: sk-1234
`,
			wantEnv: []string{"GOMODEL_MASTER_KEY=sk-1234"},
		},
		{
			name: "key read from GOMODEL_MASTER_KEY",
			litellm: `
general_settings:
  master_key: os.environ/GOMODEL_MASTER_KEY
`,
			wantServer: &serverOut{MasterKey: "${GOMODEL_MASTER_KEY}"},
		},
		{
			name: "environment provides GOMODEL_MASTER_KEY for another setting",
			litellm: `
model_list:
  - model_name: m
    litellm_params: {model: openai/gpt-4o, api_key: os.environ/GOMODEL_MASTER_KEY}
general_settings:
  master_key: sk-1234
`,
			wantEnv:     []string{"GOMODEL_MASTER_KEY_2=sk-1234"},
			wantAPIKey:  "${GOMODEL_MASTER_KEY}",
			wantServer:  &serverOut{MasterKey: "${GOMODEL_MASTER_KEY_2}"},
			wantWarning: "general_settings.master_key: the config also reads GOMODEL_MASTER_KEY, which GoModel uses as its master key in place of this one; rename that variable before starting GoModel",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, result := convertYAML(t, tt.litellm)
			assert.Equal(t, tt.wantServer, cfg.Server)
			for _, line := range tt.wantEnv {
				assert.Contains(t, "\n"+string(result.Env), "\n"+line+"\n")
			}
			if tt.wantAPIKey != "" {
				var keys []string
				for _, provider := range cfg.Providers {
					keys = append(keys, provider.APIKey)
				}
				assert.Equal(t, []string{tt.wantAPIKey}, keys)
			}
			warnings := findings(result, SeverityWarning)
			if tt.wantWarning != "" {
				assert.Contains(t, warnings, tt.wantWarning)
			} else {
				assert.Empty(t, warnings)
			}
		})
	}
}

func TestConvert_ReportsEverythingLeftBehind(t *testing.T) {
	_, result := convertYAML(t, `
router_settings:
  redis_host: redis
  some_new_router_flag: true
  context_window_fallbacks: [{a: [b]}]
litellm_settings:
  drop_params: true
general_settings:
  database_url: postgres://x
  alerting: ["slack"]
guardrails:
  - guardrail_name: pii
mcp_servers: {}
`)
	skipped := findings(result, SeveritySkipped)
	assert.Contains(t, skipped, "router_settings.some_new_router_flag: no GoModel equivalent; not migrated")
	assert.Contains(t, skipped, "router_settings.context_window_fallbacks: GoModel fails over on 429, 5xx, and model-not-found errors; add the error phrases you need to failover.retry_on_errors")
	assert.Contains(t, skipped, "mcp_servers: no GoModel equivalent; not migrated")

	info := findings(result, SeverityInfo)
	assert.Contains(t, info, "router_settings.redis_host: not needed for routing: GoModel keeps routing state in the gateway")
	assert.Contains(t, info, "litellm_settings.drop_params: not needed: GoModel adapts parameters to each provider by default")
	assert.Contains(t, info, "general_settings.alerting: GoModel has no built-in alerting; alert on its Prometheus metrics")
	warnings := findings(result, SeverityWarning)
	assert.Contains(t, warnings, "guardrails (pii): not migrated, and GoModel guardrails are off by default: rebuild them before switching traffic; see /advanced/guardrails")
	assert.Contains(t, strings.Join(warnings, "\n"), "general_settings.database_url: not reused")
}

func TestConvert_EnvironmentVariables(t *testing.T) {
	_, result := convertYAML(t, `
environment_variables:
  OPENAI_API_KEY: sk-from-section
  AWS_REGION: eu-west-1
`)
	assert.Contains(t, string(result.Env), "AWS_REGION=eu-west-1\nOPENAI_API_KEY=sk-from-section\n")
	assert.Equal(t, []string{"AWS_REGION", "OPENAI_API_KEY"}, result.Report.WrittenEnv)
}

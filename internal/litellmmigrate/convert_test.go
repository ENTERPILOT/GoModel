package litellmmigrate

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// convertYAML converts an inline LiteLLM config and decodes the generated
// GoModel config back into the converter's output types.
func convertYAML(t *testing.T, litellm string) (*gomodelConfig, *Result) {
	t.Helper()
	src, err := parse([]byte(litellm))
	require.NoError(t, err)
	result, err := convert(src, "config.yaml")
	require.NoError(t, err)
	var out gomodelConfig
	require.NoError(t, yaml.Unmarshal(result.Config, &out))
	return &out, result
}

func findings(r *Result, severity Severity) []string {
	var out []string
	for _, f := range r.Report.Findings {
		if f.Severity == severity {
			out = append(out, f.Subject+": "+f.Message)
		}
	}
	return out
}

func virtualModel(t *testing.T, cfg *gomodelConfig, source string) virtualModelOut {
	t.Helper()
	for _, vm := range cfg.VirtualModels {
		if vm.Source == source {
			return vm
		}
	}
	require.Failf(t, "virtual model not found", "source %q", source)
	return virtualModelOut{}
}

func TestConvert_ProviderNaming(t *testing.T) {
	tests := []struct {
		name       string
		params     string
		wantName   string
		wantAPIKey string
		wantEnv    string
	}{
		{name: "default key variable", params: "{model: openai/gpt-4o}", wantName: "openai", wantAPIKey: "${OPENAI_API_KEY}"},
		{name: "explicit default key variable", params: "{model: openai/gpt-4o, api_key: os.environ/OPENAI_API_KEY}", wantName: "openai", wantAPIKey: "${OPENAI_API_KEY}"},
		{name: "inline key takes the free default variable", params: "{model: openai/gpt-4o, api_key: sk-inline}", wantName: "openai", wantAPIKey: "${OPENAI_API_KEY}", wantEnv: "OPENAI_API_KEY=sk-inline"},
		{name: "suffixed variable matches GoModel naming", params: "{model: openai/gpt-4o, api_key: os.environ/OPENAI_EU_API_KEY}", wantName: "openai-eu", wantAPIKey: "${OPENAI_EU_API_KEY}"},
		{name: "unrelated variable is numbered", params: "{model: openai/gpt-4o, api_key: os.environ/PROD_KEY}", wantName: "openai-1", wantAPIKey: "${PROD_KEY}"},
		{name: "openai-compatible provider", params: "{model: mistral/mistral-large-latest}", wantName: "mistral", wantAPIKey: "${MISTRAL_API_KEY}"},
		{name: "local provider without key", params: "{model: ollama/llama3}", wantName: "ollama"},
		{name: "bare model name inferred", params: "{model: claude-sonnet-4-5}", wantName: "anthropic", wantAPIKey: "${ANTHROPIC_API_KEY}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, result := convertYAML(t, "model_list:\n  - model_name: m\n    litellm_params: "+tt.params+"\n")
			require.Contains(t, cfg.Providers, tt.wantName)
			assert.Equal(t, tt.wantAPIKey, cfg.Providers[tt.wantName].APIKey)
			if tt.wantEnv == "" {
				assert.Nil(t, result.Env)
			} else {
				assert.Contains(t, string(result.Env), tt.wantEnv)
			}
		})
	}
}

func TestConvert_InlineKeyDoesNotReuseAVariableInUse(t *testing.T) {
	cfg, result := convertYAML(t, `
model_list:
  - model_name: a
    litellm_params: {model: openai/gpt-4o, api_key: sk-inline}
  - model_name: b
    litellm_params: {model: openai/gpt-4o-mini}
`)
	require.Contains(t, cfg.Providers, "openai-1")
	assert.Equal(t, "${OPENAI_1_API_KEY}", cfg.Providers["openai-1"].APIKey)
	assert.Equal(t, "${OPENAI_API_KEY}", cfg.Providers["openai"].APIKey)
	assert.Contains(t, string(result.Env), "OPENAI_1_API_KEY=sk-inline")
	assert.NotContains(t, string(result.Config), "sk-inline")
}

func TestConvert_BaseURLs(t *testing.T) {
	tests := []struct {
		name     string
		params   string
		provider string
		want     string
	}{
		{name: "anthropic gets /v1", params: "{model: anthropic/claude-sonnet-4-5, api_base: 'https://proxy.example.com'}", provider: "anthropic", want: "https://proxy.example.com/v1"},
		{name: "anthropic full messages URL", params: "{model: anthropic/claude-sonnet-4-5, api_base: 'https://proxy.example.com/v1/messages'}", provider: "anthropic", want: "https://proxy.example.com/v1"},
		{name: "ollama default", params: "{model: ollama/llama3}", provider: "ollama", want: "http://localhost:11434/v1"},
		{name: "vllm keeps /v1", params: "{model: hosted_vllm/qwen, api_base: 'http://vllm:8000/v1/'}", provider: "vllm", want: "http://vllm:8000/v1"},
		{name: "azure binds the deployment", params: "{model: azure/gpt4o-prod, api_base: 'https://res.openai.azure.com/'}", provider: "azure-gpt4o-prod", want: "https://res.openai.azure.com/openai/deployments/gpt4o-prod"},
		{name: "azure env base", params: "{model: azure/gpt4o-prod}", provider: "azure-gpt4o-prod", want: "${AZURE_API_BASE}/openai/deployments/gpt4o-prod"},
		{name: "compatible provider default", params: "{model: together_ai/meta-llama/Llama-3-70b}", provider: "together-ai", want: "https://api.together.xyz/v1"},
		{name: "bedrock region", params: "{model: bedrock/converse/anthropic.claude-3-haiku, aws_region_name: eu-west-1}", provider: "bedrock", want: "eu-west-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, _ := convertYAML(t, "model_list:\n  - model_name: m\n    litellm_params: "+tt.params+"\n")
			require.Contains(t, cfg.Providers, tt.provider)
			assert.Equal(t, tt.want, cfg.Providers[tt.provider].BaseURL)
		})
	}
}

func TestConvert_ModelGroups(t *testing.T) {
	cfg, _ := convertYAML(t, `
model_list:
  - model_name: chat
    litellm_params: {model: openai/gpt-4o, rpm: 900}
  - model_name: chat
    litellm_params: {model: azure/gpt-4o, api_base: "https://r.openai.azure.com", rpm: 300}
  - model_name: equal
    litellm_params: {model: openai/gpt-4o-mini}
  - model_name: equal
    litellm_params: {model: groq/llama-3.3-70b}
  - model_name: dup
    litellm_params: {model: openai/gpt-4o, weight: 1}
  - model_name: dup
    litellm_params: {model: openai/gpt-4o, weight: 2}
  - model_name: dup
    litellm_params: {model: groq/llama-3.3-70b, weight: 1}
router_settings:
  routing_strategy: cost-based-routing
`)
	chat := virtualModel(t, cfg, "chat")
	assert.Equal(t, "cost", chat.Strategy)
	assert.Equal(t, []targetOut{
		{Provider: "openai", Model: "gpt-4o", Weight: 900},
		{Provider: "azure-gpt-4o", Model: "gpt-4o", Weight: 300},
	}, chat.Targets)

	equal := virtualModel(t, cfg, "equal")
	assert.Equal(t, []targetOut{{Provider: "openai", Model: "gpt-4o-mini"}, {Provider: "groq", Model: "llama-3.3-70b"}}, equal.Targets)

	dup := virtualModel(t, cfg, "dup")
	assert.Equal(t, []targetOut{{Provider: "openai", Model: "gpt-4o", Weight: 3}, {Provider: "groq", Model: "llama-3.3-70b", Weight: 1}}, dup.Targets)

	require.NotNil(t, cfg.Models)
	assert.True(t, cfg.Models.KeepOnlyAliasesAtModelsEndpoint)
	assert.Equal(t, "allowlist", cfg.Models.ConfiguredProviderModelsMode)
	assert.Equal(t, []providerModel{{ID: "gpt-4o"}, {ID: "gpt-4o-mini"}}, cfg.Providers["openai"].Models)
}

func TestConvert_Fallbacks(t *testing.T) {
	cfg, result := convertYAML(t, `
model_list:
  - model_name: primary
    litellm_params: {model: openai/gpt-4o}
  - model_name: primary
    litellm_params: {model: groq/llama-3.3-70b}
  - model_name: backup
    litellm_params: {model: anthropic/claude-sonnet-4-5}
  - model_name: single
    litellm_params: {model: openai/gpt-4o-mini}
  - model_name: other
    litellm_params: {model: openai/gpt-4.1}
router_settings:
  fallbacks:
    - primary: [backup, missing]
    - single: [primary]
  default_fallbacks: [backup]
`)
	assert.Equal(t, []targetOut{{Provider: "openai", Model: "gpt-4o"}, {Provider: "groq", Model: "llama-3.3-70b"}}, virtualModel(t, cfg, "primary-pool").Targets)

	primary := virtualModel(t, cfg, "primary")
	assert.Equal(t, "failover", primary.Strategy)
	assert.Equal(t, []targetOut{{Model: "primary-pool"}, {Provider: "anthropic", Model: "claude-sonnet-4-5"}}, primary.Targets)

	// A fallback points at the fallback group's deployments, not its own
	// failover model, so chains never loop.
	single := virtualModel(t, cfg, "single")
	assert.Equal(t, []targetOut{{Provider: "openai", Model: "gpt-4o-mini"}, {Model: "primary-pool"}}, single.Targets)

	other := virtualModel(t, cfg, "other")
	assert.Equal(t, "failover", other.Strategy, "default_fallbacks apply to groups without their own list")

	backup := virtualModel(t, cfg, "backup")
	assert.Empty(t, backup.Strategy, "a group is never its own default fallback")

	assert.Contains(t, findings(result, SeverityWarning), `fallbacks.primary: fallback "missing" is not a model_name in model_list; dropped`)
}

func TestConvert_ModelGroupAlias(t *testing.T) {
	cfg, result := convertYAML(t, `
model_list:
  - model_name: gpt-4o
    litellm_params: {model: openai/gpt-4o}
router_settings:
  model_group_alias:
    gpt-4: gpt-4o
    hidden-alias: {model: gpt-4o, hidden: true}
    broken: nope
`)
	assert.Equal(t, []targetOut{{Model: "gpt-4o"}}, virtualModel(t, cfg, "gpt-4").Targets)
	assert.Equal(t, []targetOut{{Model: "gpt-4o"}}, virtualModel(t, cfg, "hidden-alias").Targets)
	assert.Contains(t, findings(result, SeverityWarning), `model_group_alias.broken: target "nope" is not a model_name in model_list; dropped`)
	assert.Contains(t, findings(result, SeverityInfo), "model_group_alias.hidden-alias: GoModel has no hidden aliases; this one is listed in GET /v1/models")
}

func TestConvert_ModelNameThatIsAlreadyQualified(t *testing.T) {
	cfg, _ := convertYAML(t, `
model_list:
  - model_name: openai/gpt-4o
    litellm_params: {model: openai/gpt-4o}
  - model_name: fast
    litellm_params: {model: openai/gpt-4o-mini}
`)
	// A self-alias is invalid; the model is served directly instead, so the
	// models endpoint must keep listing provider models.
	require.Len(t, cfg.VirtualModels, 1)
	assert.Equal(t, "fast", cfg.VirtualModels[0].Source)
	assert.False(t, cfg.Models.KeepOnlyAliasesAtModelsEndpoint)
}

func TestConvert_Wildcards(t *testing.T) {
	cfg, result := convertYAML(t, `
model_list:
  - model_name: "openai/*"
    litellm_params: {model: "openai/*"}
  - model_name: gpt-4o
    litellm_params: {model: openai/gpt-4o, input_cost_per_token: 0.0000025}
  - model_name: "claude-*"
    litellm_params: {model: "anthropic/claude-*"}
  - model_name: haiku
    litellm_params: {model: anthropic/claude-haiku-4-5}
`)
	assert.Empty(t, cfg.Providers["openai"].Models, "a whole-catalog provider gets no allowlist")
	assert.Nil(t, cfg.Providers["openai"].ModelFilter)
	require.NotNil(t, cfg.Providers["anthropic"].ModelFilter)
	assert.Equal(t, []string{"claude-*", "claude-haiku-4-5"}, cfg.Providers["anthropic"].ModelFilter.Include)
	assert.Nil(t, cfg.Models, "neither an allowlist nor alias-only listing applies")
	assert.Contains(t, findings(result, SeverityWarning), `model_list[2] (claude-*): clients that called "claude-*" now address these models as anthropic/<model>`)
	assert.Contains(t, strings.Join(findings(result, SeverityWarning), "\n"), "pricing and token limits for gpt-4o were not migrated")
}

func TestConvert_CatchAllDeployment(t *testing.T) {
	cfg, result := convertYAML(t, `
model_list:
  - model_name: "*"
    litellm_params: {model: "*"}
`)
	assert.Empty(t, cfg.Providers)
	assert.Empty(t, findings(result, SeveritySkipped))
	assert.Contains(t, findings(result, SeverityInfo)[0], "a catch-all `*` deployment needs no config")
}

func TestConvert_UnsupportedDeployments(t *testing.T) {
	cfg, result := convertYAML(t, `
model_list:
  - model_name: r
    litellm_params: {model: replicate/meta/llama-2-70b}
  - model_name: s
    litellm_params: {model: sagemaker/my-endpoint, custom_llm_provider: sagemaker}
  - model_name: ok
    litellm_params: {model: openai/gpt-4o, timeout: 30, stream_timeout: 60}
`)
	assert.Len(t, cfg.Providers, 1)
	skipped := findings(result, SeveritySkipped)
	assert.Contains(t, skipped, `model_list[0] (r): cannot tell which provider serves "replicate/meta/llama-2-70b"; add a provider prefix such as openai/ and run again`)
	assert.Contains(t, skipped, `model_list[1] (s): LiteLLM provider "sagemaker" has no GoModel equivalent yet`)
	assert.Contains(t, skipped, "model_list[2] (ok): not migrated: litellm_params.stream_timeout, litellm_params.timeout")
}

func TestConvert_ModelMetadata(t *testing.T) {
	cfg, result := convertYAML(t, `
model_list:
  - model_name: priced
    litellm_params: {model: openai/my-ft, input_cost_per_token: 0.000001}
    model_info:
      input_cost_per_token: 0.0000025
      output_cost_per_token: 0.00001
      cache_read_input_token_cost: 0.00000125
      max_input_tokens: 128000
  - model_name: again
    litellm_params: {model: openai/my-ft}
    model_info: {output_cost_per_token: 0.00002}
`)
	models := cfg.Providers["openai"].Models
	require.Len(t, models, 1)
	meta := models[0].Metadata
	require.NotNil(t, meta)
	require.NotNil(t, meta.Pricing)
	assert.Equal(t, "USD", meta.Pricing.Currency)
	assert.Equal(t, 2.5, *meta.Pricing.InputPerMtok, "model_info wins over litellm_params")
	assert.Equal(t, 10.0, *meta.Pricing.OutputPerMtok)
	assert.Equal(t, 1.25, *meta.Pricing.CachedInputPerMtok)
	assert.Equal(t, 128000, *meta.ContextWindow)
	assert.Contains(t, findings(result, SeverityWarning), "model_list[1] (again): openai/my-ft already has pricing or limits from an earlier deployment; kept the first")
}

func TestConvert_UsageBasedRoutingEnforcesLimits(t *testing.T) {
	cfg, _ := convertYAML(t, `
model_list:
  - model_name: a
    litellm_params: {model: openai/gpt-4o, rpm: 100, tpm: 50000}
  - model_name: b
    litellm_params: {model: openai/gpt-4o, rpm: 60}
router_settings:
  routing_strategy: usage-based-routing-v2
`)
	require.NotNil(t, cfg.RateLimits)
	assert.Equal(t, []modelRateLimitOut{{
		Model:  "openai/gpt-4o",
		Limits: []rateLimitOut{{Period: "minute", MaxRequests: 60, MaxTokens: 50000}},
	}}, cfg.RateLimits.Models)

	plain, _ := convertYAML(t, `
model_list:
  - model_name: a
    litellm_params: {model: openai/gpt-4o, rpm: 100}
`)
	assert.Nil(t, plain.RateLimits, "outside usage-based routing rpm only weights load balancing")
}

func TestConvert_CredentialList(t *testing.T) {
	cfg, _ := convertYAML(t, `
credential_list:
  - credential_name: eu
    credential_values:
      api_key: os.environ/AZURE_EU_API_KEY
      api_base: https://eu.openai.azure.com
      api_version: "2025-01-01"
model_list:
  - model_name: m
    litellm_params: {model: azure/gpt-4o, litellm_credential_name: eu}
`)
	p := cfg.Providers["azure-gpt-4o"]
	require.NotNil(t, p)
	assert.Equal(t, "${AZURE_EU_API_KEY}", p.APIKey)
	assert.Equal(t, "https://eu.openai.azure.com/openai/deployments/gpt-4o", p.BaseURL)
	assert.Equal(t, "2025-01-01", p.APIVersion)
}

func TestConvert_VertexCredentials(t *testing.T) {
	cfg, result := convertYAML(t, `
model_list:
  - model_name: file
    litellm_params: {model: vertex_ai/gemini-2.5-pro, vertex_project: p1, vertex_location: us-central1, vertex_credentials: /secrets/sa.json}
  - model_name: inline
    litellm_params:
      model: vertex_ai/gemini-2.5-flash
      vertex_project: p2
      vertex_credentials: {type: service_account, private_key: "a\nb"}
`)
	file := cfg.Providers["vertex"]
	require.NotNil(t, file)
	assert.Equal(t, "gcp_service_account", file.AuthType)
	assert.Equal(t, "/secrets/sa.json", file.ServiceAccountFile)
	assert.Equal(t, "p1", file.VertexProject)

	inline := cfg.Providers["vertex-1"]
	require.NotNil(t, inline)
	assert.Equal(t, "${VERTEX_1_SERVICE_ACCOUNT_JSON}", inline.ServiceAccountJSON)
	assert.Contains(t, string(result.Env), "VERTEX_1_SERVICE_ACCOUNT_JSON=")
}

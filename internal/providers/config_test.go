package providers

import (
	"testing"
	"time"

	"gomodel/config"
)

var globalRetry = config.RetryConfig{
	MaxRetries:     3,
	InitialBackoff: 1 * time.Second,
	MaxBackoff:     30 * time.Second,
	BackoffFactor:  2.0,
	JitterFactor:   0.1,
}

var globalResilience = config.ResilienceConfig{Retry: globalRetry}

var testDiscoveryConfigs = map[string]DiscoveryConfig{
	"openai": {
		DefaultBaseURL: "https://api.openai.com/v1",
	},
	"anthropic": {
		DefaultBaseURL: "https://api.anthropic.com/v1",
	},
	"gemini": {
		DefaultBaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
	},
	"xai": {
		DefaultBaseURL: "https://api.x.ai/v1",
	},
	"groq": {
		DefaultBaseURL: "https://api.groq.com/openai/v1",
	},
	"openrouter": {
		DefaultBaseURL: "https://openrouter.ai/api/v1",
	},
	"zai": {
		DefaultBaseURL: "https://api.z.ai/api/paas/v4",
	},
	"vllm": {
		DefaultBaseURL:  "http://localhost:8000/v1",
		AllowAPIKeyless: true,
	},
	"azure": {
		RequireBaseURL:     true,
		SupportsAPIVersion: true,
	},
	"oracle": {
		RequireBaseURL:    true,
		SupportsModelsEnv: true,
	},
	"ollama": {
		DefaultBaseURL:  "http://localhost:11434/v1",
		AllowAPIKeyless: true,
	},
}

// --- buildProviderConfig ---

func TestBuildProviderConfig_InheritsGlobal(t *testing.T) {
	raw := config.RawProviderConfig{Type: "openai", APIKey: "sk-test"}
	got := buildProviderConfig(raw, globalResilience)

	if got.Type != "openai" {
		t.Errorf("Type = %q, want openai", got.Type)
	}
	if got.Resilience.Retry != globalRetry {
		t.Errorf("expected global retry to be inherited\ngot:  %+v\nwant: %+v", got.Resilience.Retry, globalRetry)
	}
}

func TestBuildProviderConfig_NilResilience(t *testing.T) {
	raw := config.RawProviderConfig{Type: "openai", APIKey: "sk", Resilience: nil}
	got := buildProviderConfig(raw, globalResilience)

	if got.Resilience.Retry != globalRetry {
		t.Error("nil Resilience should inherit global")
	}
}

func TestBuildProviderConfig_NilRetry(t *testing.T) {
	raw := config.RawProviderConfig{
		Type:       "openai",
		APIKey:     "sk",
		Resilience: &config.RawResilienceConfig{Retry: nil},
	}
	got := buildProviderConfig(raw, globalResilience)

	if got.Resilience.Retry != globalRetry {
		t.Error("nil Retry should inherit global")
	}
}

func TestBuildProviderConfig_PartialOverride(t *testing.T) {
	raw := config.RawProviderConfig{
		Type:   "anthropic",
		APIKey: "sk-ant",
		Resilience: &config.RawResilienceConfig{
			Retry: &config.RawRetryConfig{
				MaxRetries: new(10),
			},
		},
	}
	got := buildProviderConfig(raw, globalResilience)

	if got.Resilience.Retry.MaxRetries != 10 {
		t.Errorf("MaxRetries = %d, want 10", got.Resilience.Retry.MaxRetries)
	}
	if got.Resilience.Retry.InitialBackoff != globalRetry.InitialBackoff {
		t.Errorf("InitialBackoff should be inherited, got %v", got.Resilience.Retry.InitialBackoff)
	}
	if got.Resilience.Retry.JitterFactor != globalRetry.JitterFactor {
		t.Errorf("JitterFactor should be inherited, got %f", got.Resilience.Retry.JitterFactor)
	}
}

func TestBuildProviderConfig_FullOverride(t *testing.T) {
	raw := config.RawProviderConfig{
		Type:   "gemini",
		APIKey: "sk-gem",
		Resilience: &config.RawResilienceConfig{
			Retry: &config.RawRetryConfig{
				MaxRetries:     new(7),
				InitialBackoff: new(500 * time.Millisecond),
				MaxBackoff:     new(10 * time.Second),
				BackoffFactor:  new(1.5),
				JitterFactor:   new(0.3),
			},
		},
	}
	got := buildProviderConfig(raw, globalResilience)

	r := got.Resilience.Retry
	if r.MaxRetries != 7 {
		t.Errorf("MaxRetries = %d, want 7", r.MaxRetries)
	}
	if r.InitialBackoff != 500*time.Millisecond {
		t.Errorf("InitialBackoff = %v, want 500ms", r.InitialBackoff)
	}
	if r.MaxBackoff != 10*time.Second {
		t.Errorf("MaxBackoff = %v, want 10s", r.MaxBackoff)
	}
	if r.BackoffFactor != 1.5 {
		t.Errorf("BackoffFactor = %f, want 1.5", r.BackoffFactor)
	}
	if r.JitterFactor != 0.3 {
		t.Errorf("JitterFactor = %f, want 0.3", r.JitterFactor)
	}
}

func TestBuildProviderConfig_ZeroValueOverride(t *testing.T) {
	raw := config.RawProviderConfig{
		Type:   "groq",
		APIKey: "sk-groq",
		Resilience: &config.RawResilienceConfig{
			Retry: &config.RawRetryConfig{
				MaxRetries: new(0),
			},
		},
	}
	got := buildProviderConfig(raw, globalResilience)

	if got.Resilience.Retry.MaxRetries != 0 {
		t.Errorf("explicit 0 should override global (3), got %d", got.Resilience.Retry.MaxRetries)
	}
}

func TestBuildProviderConfig_PreservesFields(t *testing.T) {
	raw := config.RawProviderConfig{
		Type:    "openai",
		APIKey:  "sk-key",
		BaseURL: "https://custom.endpoint.com",
		Models:  []string{"gpt-4", "gpt-3.5-turbo"},
	}
	got := buildProviderConfig(raw, globalResilience)

	if got.APIKey != "sk-key" {
		t.Errorf("APIKey = %q, want sk-key", got.APIKey)
	}
	if got.BaseURL != "https://custom.endpoint.com" {
		t.Errorf("BaseURL = %q, want https://custom.endpoint.com", got.BaseURL)
	}
	if len(got.Models) != 2 || got.Models[0] != "gpt-4" {
		t.Errorf("Models = %v, want [gpt-4 gpt-3.5-turbo]", got.Models)
	}
}

// --- buildProviderConfigs ---

func TestBuildProviderConfigs_MultipleProviders(t *testing.T) {
	maxRetries := 10
	raw := map[string]config.RawProviderConfig{
		"openai": {
			Type:   "openai",
			APIKey: "sk-openai",
			Resilience: &config.RawResilienceConfig{
				Retry: &config.RawRetryConfig{MaxRetries: &maxRetries},
			},
		},
		"anthropic": {Type: "anthropic", APIKey: "sk-ant"},
	}

	got := buildProviderConfigs(raw, globalResilience)

	if got["openai"].Resilience.Retry.MaxRetries != 10 {
		t.Errorf("openai MaxRetries = %d, want 10", got["openai"].Resilience.Retry.MaxRetries)
	}
	if got["anthropic"].Resilience.Retry.MaxRetries != globalRetry.MaxRetries {
		t.Errorf("anthropic MaxRetries = %d, want %d (global)", got["anthropic"].Resilience.Retry.MaxRetries, globalRetry.MaxRetries)
	}
}

func TestBuildProviderConfigs_EmptyMap(t *testing.T) {
	got := buildProviderConfigs(map[string]config.RawProviderConfig{}, globalResilience)
	if len(got) != 0 {
		t.Errorf("expected empty result, got %d entries", len(got))
	}
}

// --- filterEmptyProviders ---

func TestFilterEmptyProviders_RemovesEmptyAPIKey(t *testing.T) {
	raw := map[string]config.RawProviderConfig{
		"openai":    {Type: "openai", APIKey: ""},
		"anthropic": {Type: "anthropic", APIKey: "sk-ant"},
	}
	got := filterEmptyProviders(raw, testDiscoveryConfigs)

	if _, exists := got["openai"]; exists {
		t.Error("expected openai with empty API key to be removed")
	}
	if _, exists := got["anthropic"]; !exists {
		t.Error("expected anthropic to be kept")
	}
}

func TestFilterEmptyProviders_RemovesUnresolvedPlaceholder(t *testing.T) {
	raw := map[string]config.RawProviderConfig{
		"openai":    {Type: "openai", APIKey: "${OPENAI_API_KEY}"},
		"anthropic": {Type: "anthropic", APIKey: "sk-real"},
	}
	got := filterEmptyProviders(raw, testDiscoveryConfigs)

	if _, exists := got["openai"]; exists {
		t.Error("expected openai with unresolved placeholder to be removed")
	}
	if _, exists := got["anthropic"]; !exists {
		t.Error("expected anthropic to survive filtering")
	}
}

func TestFilterEmptyProviders_RemovesPartialPlaceholder(t *testing.T) {
	raw := map[string]config.RawProviderConfig{
		"openai": {Type: "openai", APIKey: "prefix-${UNRESOLVED}"},
	}
	got := filterEmptyProviders(raw, testDiscoveryConfigs)

	if _, exists := got["openai"]; exists {
		t.Error("expected provider with partial placeholder to be removed")
	}
}

func TestFilterEmptyProviders_OllamaAlwaysKept(t *testing.T) {
	cases := []struct {
		name string
		raw  config.RawProviderConfig
	}{
		{"no credentials", config.RawProviderConfig{Type: "ollama"}},
		{"with base url", config.RawProviderConfig{Type: "ollama", BaseURL: "http://localhost:11434/v1"}},
		{"with api key and base url", config.RawProviderConfig{Type: "ollama", APIKey: "sk-ollama", BaseURL: "http://localhost:11434/v1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterEmptyProviders(map[string]config.RawProviderConfig{"ollama": tc.raw}, testDiscoveryConfigs)
			if _, exists := got["ollama"]; !exists {
				t.Errorf("expected ollama to be kept (%s)", tc.name)
			}
		})
	}
}

func TestFilterEmptyProviders_VLLMAllowsKeylessConfig(t *testing.T) {
	got := filterEmptyProviders(map[string]config.RawProviderConfig{
		"vllm": {Type: "vllm", BaseURL: "http://localhost:8000/v1"},
	}, testDiscoveryConfigs)
	if _, exists := got["vllm"]; !exists {
		t.Fatal("expected vllm to be kept without an API key")
	}
}

func TestFilterEmptyProviders_EmptyMap(t *testing.T) {
	got := filterEmptyProviders(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)
	if len(got) != 0 {
		t.Errorf("expected empty result, got %d entries", len(got))
	}
}

func TestFilterEmptyProviders_RemovesAzureByTypeWithoutBaseURL(t *testing.T) {
	raw := map[string]config.RawProviderConfig{
		"my-azure": {Type: "azure", APIKey: "sk-azure"},
	}

	got := filterEmptyProviders(raw, testDiscoveryConfigs)

	if _, exists := got["my-azure"]; exists {
		t.Fatal("expected azure provider without base URL to be removed regardless of map key")
	}
}

func TestFilterEmptyProviders_RemovesOracleByTypeWithoutBaseURL(t *testing.T) {
	raw := map[string]config.RawProviderConfig{
		"oracle-primary": {Type: "oracle", APIKey: "oracle-key"},
	}

	got := filterEmptyProviders(raw, testDiscoveryConfigs)

	if _, exists := got["oracle-primary"]; exists {
		t.Fatal("expected oracle provider without base URL to be removed regardless of map key")
	}
}

// --- applyProviderEnvVars ---

func TestApplyProviderEnvVars_DiscoversFromAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-from-env")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	p, exists := got["openai"]
	if !exists {
		t.Fatal("expected openai to be discovered from env var")
	}
	if p.APIKey != "sk-from-env" {
		t.Errorf("APIKey = %q, want sk-from-env", p.APIKey)
	}
	if p.Type != "openai" {
		t.Errorf("Type = %q, want openai", p.Type)
	}
}

func TestApplyProviderEnvVars_DiscoversFromBaseURL(t *testing.T) {
	t.Setenv("OLLAMA_BASE_URL", "http://localhost:11434/v1")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	p, exists := got["ollama"]
	if !exists {
		t.Fatal("expected ollama to be discovered from base URL env var")
	}
	if p.BaseURL != "http://localhost:11434/v1" {
		t.Errorf("BaseURL = %q, want http://localhost:11434/v1", p.BaseURL)
	}
}

func TestApplyProviderEnvVars_DiscoversOpenRouterFromAPIKey(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-openrouter")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	p, exists := got["openrouter"]
	if !exists {
		t.Fatal("expected openrouter to be discovered from env var")
	}
	if p.APIKey != "sk-openrouter" {
		t.Errorf("APIKey = %q, want sk-openrouter", p.APIKey)
	}
	if p.Type != "openrouter" {
		t.Errorf("Type = %q, want openrouter", p.Type)
	}
	if p.BaseURL != testDiscoveryConfigs["openrouter"].DefaultBaseURL {
		t.Errorf("BaseURL = %q, want %q", p.BaseURL, testDiscoveryConfigs["openrouter"].DefaultBaseURL)
	}
}

func TestApplyProviderEnvVars_DiscoversZAIFromAPIKey(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "zai-key")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	p, exists := got["zai"]
	if !exists {
		t.Fatal("expected zai to be discovered from env var")
	}
	if p.APIKey != "zai-key" {
		t.Errorf("APIKey = %q, want zai-key", p.APIKey)
	}
	if p.Type != "zai" {
		t.Errorf("Type = %q, want zai", p.Type)
	}
	if p.BaseURL != testDiscoveryConfigs["zai"].DefaultBaseURL {
		t.Errorf("BaseURL = %q, want %q", p.BaseURL, testDiscoveryConfigs["zai"].DefaultBaseURL)
	}
}

func TestApplyProviderEnvVars_DiscoversZAIWithExplicitBaseURL(t *testing.T) {
	const explicitBaseURL = "https://api.z.ai/api/coding/paas/v4"
	t.Setenv("ZAI_API_KEY", "zai-key")
	t.Setenv("ZAI_BASE_URL", explicitBaseURL)

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	p, exists := got["zai"]
	if !exists {
		t.Fatal("expected zai to be discovered from env var")
	}
	if p.APIKey != "zai-key" {
		t.Errorf("APIKey = %q, want zai-key", p.APIKey)
	}
	if p.Type != "zai" {
		t.Errorf("Type = %q, want zai", p.Type)
	}
	if p.BaseURL != explicitBaseURL {
		t.Errorf("BaseURL = %q, want %q", p.BaseURL, explicitBaseURL)
	}
}

func TestApplyProviderEnvVars_DiscoversVLLMFromBaseURLWithoutAPIKey(t *testing.T) {
	t.Setenv("VLLM_BASE_URL", "http://localhost:8000/v1")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	p, exists := got["vllm"]
	if !exists {
		t.Fatal("expected vllm to be discovered from base URL env var")
	}
	if p.APIKey != "" {
		t.Errorf("APIKey = %q, want empty", p.APIKey)
	}
	if p.Type != "vllm" {
		t.Errorf("Type = %q, want vllm", p.Type)
	}
	if p.BaseURL != "http://localhost:8000/v1" {
		t.Errorf("BaseURL = %q, want http://localhost:8000/v1", p.BaseURL)
	}
}

func TestApplyProviderEnvVars_DiscoversVLLMFromAPIKeyWithDefaultBaseURL(t *testing.T) {
	t.Setenv("VLLM_API_KEY", "vllm-key")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	p, exists := got["vllm"]
	if !exists {
		t.Fatal("expected vllm to be discovered from API key env var")
	}
	if p.APIKey != "vllm-key" {
		t.Errorf("APIKey = %q, want vllm-key", p.APIKey)
	}
	if p.BaseURL != testDiscoveryConfigs["vllm"].DefaultBaseURL {
		t.Errorf("BaseURL = %q, want %q", p.BaseURL, testDiscoveryConfigs["vllm"].DefaultBaseURL)
	}
}

func TestApplyProviderEnvVars_IgnoresVLLMModelsEnv(t *testing.T) {
	t.Setenv("VLLM_MODELS", "meta-llama/Llama-3.1-8B-Instruct")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	if _, exists := got["vllm"]; exists {
		t.Fatal("expected VLLM_MODELS not to discover vllm provider")
	}
}

func TestApplyProviderEnvVars_DiscoversAzureFromExplicitEnvVars(t *testing.T) {
	t.Setenv("AZURE_API_KEY", "sk-azure")
	t.Setenv("AZURE_BASE_URL", "https://example-resource.openai.azure.com/openai/deployments/gpt-4o")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	p, exists := got["azure"]
	if !exists {
		t.Fatal("expected azure to be discovered from env vars")
	}
	if p.APIKey != "sk-azure" {
		t.Errorf("APIKey = %q, want sk-azure", p.APIKey)
	}
	if p.Type != "azure" {
		t.Errorf("Type = %q, want azure", p.Type)
	}
	if p.BaseURL != "https://example-resource.openai.azure.com/openai/deployments/gpt-4o" {
		t.Errorf("BaseURL = %q, want Azure API base", p.BaseURL)
	}
}

func TestApplyProviderEnvVars_AzureAPIVersionEnvWins(t *testing.T) {
	t.Setenv("AZURE_API_KEY", "sk-azure")
	t.Setenv("AZURE_BASE_URL", "https://example-resource.openai.azure.com/openai/deployments/gpt-4o")
	t.Setenv("AZURE_API_VERSION", "2025-04-01-preview")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	p, exists := got["azure"]
	if !exists {
		t.Fatal("expected azure to be discovered from env vars")
	}
	if p.APIVersion != "2025-04-01-preview" {
		t.Errorf("APIVersion = %q, want 2025-04-01-preview", p.APIVersion)
	}
}

func TestApplyProviderEnvVars_AzureAPIVersionEnvWinsWithoutOtherAzureEnvVars(t *testing.T) {
	t.Setenv("AZURE_API_VERSION", "2025-04-01-preview")

	raw := map[string]config.RawProviderConfig{
		"azure": {
			Type:       "azure",
			APIKey:     "sk-yaml-azure",
			BaseURL:    "https://example-resource.openai.azure.com/openai/deployments/gpt-4o",
			APIVersion: "2024-10-21",
		},
	}

	got := applyProviderEnvVars(raw, testDiscoveryConfigs)

	if got["azure"].APIVersion != "2025-04-01-preview" {
		t.Fatalf("APIVersion = %q, want 2025-04-01-preview", got["azure"].APIVersion)
	}
}

func TestApplyProviderEnvVars_DoesNotDiscoverAzureWithoutBaseURL(t *testing.T) {
	t.Setenv("AZURE_API_KEY", "sk-azure")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	if _, exists := got["azure"]; exists {
		t.Fatal("expected azure not to be discovered without AZURE_BASE_URL")
	}
}

func TestApplyProviderEnvVars_DiscoversOracleFromExplicitEnvVars(t *testing.T) {
	t.Setenv("ORACLE_API_KEY", "oracle-key")
	t.Setenv("ORACLE_BASE_URL", "https://inference.generativeai.us-chicago-1.oci.oraclecloud.com/20231130/actions/v1")
	t.Setenv("ORACLE_MODELS", " openai.gpt-oss-120b, xai.grok-3 ,, ")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	p, exists := got["oracle"]
	if !exists {
		t.Fatal("expected oracle to be discovered from env vars")
	}
	if p.APIKey != "oracle-key" {
		t.Errorf("APIKey = %q, want oracle-key", p.APIKey)
	}
	if p.Type != "oracle" {
		t.Errorf("Type = %q, want oracle", p.Type)
	}
	if p.BaseURL != "https://inference.generativeai.us-chicago-1.oci.oraclecloud.com/20231130/actions/v1" {
		t.Errorf("BaseURL = %q, want Oracle base URL", p.BaseURL)
	}
	if len(p.Models) != 2 || p.Models[0] != "openai.gpt-oss-120b" || p.Models[1] != "xai.grok-3" {
		t.Errorf("Models = %v, want [openai.gpt-oss-120b xai.grok-3]", p.Models)
	}
}

func TestApplyProviderEnvVars_DoesNotDiscoverOracleWithoutBaseURL(t *testing.T) {
	t.Setenv("ORACLE_API_KEY", "oracle-key")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	if _, exists := got["oracle"]; exists {
		t.Fatal("expected oracle not to be discovered without ORACLE_BASE_URL")
	}
}

func TestApplyProviderEnvVars_OracleModelsEnvWinsOverYAMLWithoutOtherOracleEnvVars(t *testing.T) {
	raw := map[string]config.RawProviderConfig{
		"oracle": {
			Type:    "oracle",
			APIKey:  "oracle-key",
			BaseURL: "https://inference.generativeai.us-chicago-1.oci.oraclecloud.com/20231130/actions/v1",
			Models:  []string{"yaml-model"},
		},
	}
	t.Setenv("ORACLE_MODELS", "openai.gpt-oss-120b, xai.grok-3")

	got := applyProviderEnvVars(raw, testDiscoveryConfigs)

	p := got["oracle"]
	if p.APIKey != "oracle-key" {
		t.Fatalf("APIKey = %q, want oracle-key", p.APIKey)
	}
	if p.BaseURL != "https://inference.generativeai.us-chicago-1.oci.oraclecloud.com/20231130/actions/v1" {
		t.Fatalf("BaseURL = %q, want Oracle base URL", p.BaseURL)
	}
	if len(p.Models) != 2 || p.Models[0] != "openai.gpt-oss-120b" || p.Models[1] != "xai.grok-3" {
		t.Fatalf("Models = %v, want [openai.gpt-oss-120b xai.grok-3]", p.Models)
	}
}

func TestApplyProviderEnvVars_EnvWinsOverYAML(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-env-key")

	raw := map[string]config.RawProviderConfig{
		"openai": {Type: "openai", APIKey: "sk-yaml-key", BaseURL: "https://custom.api.com"},
	}
	got := applyProviderEnvVars(raw, testDiscoveryConfigs)

	if got["openai"].APIKey != "sk-env-key" {
		t.Errorf("APIKey = %q, want sk-env-key (env should win over YAML)", got["openai"].APIKey)
	}
	if got["openai"].BaseURL != "https://custom.api.com" {
		t.Error("BaseURL from YAML should be preserved when env var is absent")
	}
}

func TestApplyProviderEnvVars_SingleCustomNamedProviderUsesTypeEnvVars(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-env-key")

	raw := map[string]config.RawProviderConfig{
		"openai_name": {Type: "openai"},
	}
	got := applyProviderEnvVars(raw, testDiscoveryConfigs)

	provider, exists := got["openai_name"]
	if !exists {
		t.Fatal("expected custom-named openai provider to be preserved")
	}
	if provider.APIKey != "sk-env-key" {
		t.Errorf("APIKey = %q, want sk-env-key", provider.APIKey)
	}
	if provider.BaseURL != testDiscoveryConfigs["openai"].DefaultBaseURL {
		t.Errorf("BaseURL = %q, want %q", provider.BaseURL, testDiscoveryConfigs["openai"].DefaultBaseURL)
	}
	if _, exists := got["openai"]; exists {
		t.Fatal("expected no duplicate auto-discovered openai provider")
	}
}

func TestApplyProviderEnvVars_AmbiguousCustomNamedProvidersSkipTypeEnvOverlay(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-env-key")

	raw := map[string]config.RawProviderConfig{
		"openai-east": {Type: "openai", APIKey: "east-key", BaseURL: "https://east.example.com/v1"},
		"openai-west": {Type: "openai", APIKey: "west-key", BaseURL: "https://west.example.com/v1"},
	}
	got := applyProviderEnvVars(raw, testDiscoveryConfigs)

	if got["openai-east"].APIKey != "east-key" {
		t.Errorf("openai-east APIKey = %q, want east-key", got["openai-east"].APIKey)
	}
	if got["openai-west"].APIKey != "west-key" {
		t.Errorf("openai-west APIKey = %q, want west-key", got["openai-west"].APIKey)
	}
	if _, exists := got["openai"]; exists {
		t.Fatal("expected no duplicate auto-discovered openai provider when multiple YAML providers share the type")
	}
}

func TestApplyProviderEnvVars_BaseURLEnvWinsOverYAML(t *testing.T) {
	t.Setenv("OPENAI_BASE_URL", "https://env-override.com")

	raw := map[string]config.RawProviderConfig{
		"openai": {Type: "openai", APIKey: "sk-key", BaseURL: "https://yaml-url.com"},
	}
	got := applyProviderEnvVars(raw, testDiscoveryConfigs)

	if got["openai"].BaseURL != "https://env-override.com" {
		t.Errorf("BaseURL = %q, want https://env-override.com", got["openai"].BaseURL)
	}
}

func TestApplyProviderEnvVars_DefaultBaseReplacesPlaceholderYAMLBaseURL(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-openrouter")

	raw := map[string]config.RawProviderConfig{
		"openrouter": {Type: "openrouter", APIKey: "sk-yaml", BaseURL: "${OPENROUTER_BASE_URL}"},
	}

	got := applyProviderEnvVars(raw, testDiscoveryConfigs)

	if got["openrouter"].BaseURL != testDiscoveryConfigs["openrouter"].DefaultBaseURL {
		t.Fatalf("BaseURL = %q, want %q", got["openrouter"].BaseURL, testDiscoveryConfigs["openrouter"].DefaultBaseURL)
	}
}

func TestApplyProviderEnvVars_PlaceholderBaseURLEnvFallsBackToDefault(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-openrouter")
	t.Setenv("OPENROUTER_BASE_URL", "${OPENROUTER_BASE_URL}")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	if got["openrouter"].BaseURL != testDiscoveryConfigs["openrouter"].DefaultBaseURL {
		t.Fatalf("BaseURL = %q, want %q", got["openrouter"].BaseURL, testDiscoveryConfigs["openrouter"].DefaultBaseURL)
	}
}

func TestApplyProviderEnvVars_DoesNotDiscoverAzureWithPlaceholderBaseURL(t *testing.T) {
	t.Setenv("AZURE_API_KEY", "sk-azure")
	t.Setenv("AZURE_BASE_URL", "${AZURE_BASE_URL}")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	if _, exists := got["azure"]; exists {
		t.Fatal("expected azure not to be discovered with placeholder AZURE_BASE_URL")
	}
}

func TestApplyProviderEnvVars_PreservesYAMLResilience(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-env-key")

	maxRetries := 10
	raw := map[string]config.RawProviderConfig{
		"openai": {
			Type:   "openai",
			APIKey: "sk-yaml-key",
			Resilience: &config.RawResilienceConfig{
				Retry: &config.RawRetryConfig{MaxRetries: &maxRetries},
			},
		},
	}
	got := applyProviderEnvVars(raw, testDiscoveryConfigs)

	if got["openai"].Resilience == nil || got["openai"].Resilience.Retry == nil {
		t.Fatal("expected YAML resilience to be preserved after env var overlay")
	}
	if *got["openai"].Resilience.Retry.MaxRetries != 10 {
		t.Errorf("MaxRetries = %d, want 10", *got["openai"].Resilience.Retry.MaxRetries)
	}
}

func TestApplyProviderEnvVars_SkipsWhenNoEnvVars(t *testing.T) {
	// Ensure no ambient env vars interfere
	for providerType, spec := range testDiscoveryConfigs {
		envNames := derivedEnvNames(providerType)
		t.Setenv(envNames.APIKey, "")
		t.Setenv(envNames.BaseURL, "")
		t.Setenv(envNames.Models, "")
		if spec.SupportsAPIVersion {
			t.Setenv(envNames.APIVersion, "")
		}
	}
	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)
	if len(got) != 0 {
		t.Errorf("expected empty result when no env vars set, got %d entries", len(got))
	}
}

func TestApplyProviderEnvVars_PreservesUnknownYAMLProviders(t *testing.T) {
	raw := map[string]config.RawProviderConfig{
		"custom-provider": {Type: "custom", APIKey: "sk-custom"},
	}
	got := applyProviderEnvVars(raw, testDiscoveryConfigs)

	if _, exists := got["custom-provider"]; !exists {
		t.Error("expected custom (non-registered) YAML provider to be preserved")
	}
}

// --- buildProviderConfig: circuit breaker ---

func TestBuildProviderConfig_CircuitBreaker_InheritsGlobal(t *testing.T) {
	global := globalResilience
	global.CircuitBreaker = config.CircuitBreakerConfig{
		FailureThreshold: 5,
		SuccessThreshold: 2,
		Timeout:          30 * time.Second,
	}
	raw := config.RawProviderConfig{Type: "openai", APIKey: "sk"}
	got := buildProviderConfig(raw, global)

	if got.Resilience.CircuitBreaker != global.CircuitBreaker {
		t.Errorf("expected global circuit breaker to be inherited\ngot:  %+v\nwant: %+v",
			got.Resilience.CircuitBreaker, global.CircuitBreaker)
	}
}

func TestBuildProviderConfig_CircuitBreaker_NilOverride(t *testing.T) {
	global := globalResilience
	global.CircuitBreaker = config.DefaultCircuitBreakerConfig()
	raw := config.RawProviderConfig{
		Type:       "openai",
		APIKey:     "sk",
		Resilience: &config.RawResilienceConfig{CircuitBreaker: nil},
	}
	got := buildProviderConfig(raw, global)

	if got.Resilience.CircuitBreaker != global.CircuitBreaker {
		t.Error("nil CircuitBreaker override should inherit global")
	}
}

func TestBuildProviderConfig_CircuitBreaker_PartialOverride(t *testing.T) {
	global := globalResilience
	global.CircuitBreaker = config.DefaultCircuitBreakerConfig()

	failureThreshold := 10
	raw := config.RawProviderConfig{
		Type:   "openai",
		APIKey: "sk",
		Resilience: &config.RawResilienceConfig{
			CircuitBreaker: &config.RawCircuitBreakerConfig{
				FailureThreshold: &failureThreshold,
			},
		},
	}
	got := buildProviderConfig(raw, global)

	if got.Resilience.CircuitBreaker.FailureThreshold != 10 {
		t.Errorf("FailureThreshold = %d, want 10", got.Resilience.CircuitBreaker.FailureThreshold)
	}
	if got.Resilience.CircuitBreaker.SuccessThreshold != global.CircuitBreaker.SuccessThreshold {
		t.Errorf("SuccessThreshold should be inherited, got %d", got.Resilience.CircuitBreaker.SuccessThreshold)
	}
	if got.Resilience.CircuitBreaker.Timeout != global.CircuitBreaker.Timeout {
		t.Errorf("Timeout should be inherited, got %v", got.Resilience.CircuitBreaker.Timeout)
	}
}

func TestBuildProviderConfig_CircuitBreaker_FullOverride(t *testing.T) {
	global := globalResilience
	global.CircuitBreaker = config.DefaultCircuitBreakerConfig()

	failureThreshold := 3
	successThreshold := 1
	timeout := 10 * time.Second

	raw := config.RawProviderConfig{
		Type:   "openai",
		APIKey: "sk",
		Resilience: &config.RawResilienceConfig{
			CircuitBreaker: &config.RawCircuitBreakerConfig{
				FailureThreshold: &failureThreshold,
				SuccessThreshold: &successThreshold,
				Timeout:          &timeout,
			},
		},
	}
	got := buildProviderConfig(raw, global)

	cb := got.Resilience.CircuitBreaker
	if cb.FailureThreshold != 3 {
		t.Errorf("FailureThreshold = %d, want 3", cb.FailureThreshold)
	}
	if cb.SuccessThreshold != 1 {
		t.Errorf("SuccessThreshold = %d, want 1", cb.SuccessThreshold)
	}
	if cb.Timeout != 10*time.Second {
		t.Errorf("Timeout = %v, want 10s", cb.Timeout)
	}
}

func TestBuildProviderConfig_CircuitBreaker_ZeroValueOverride(t *testing.T) {
	global := globalResilience
	global.CircuitBreaker = config.DefaultCircuitBreakerConfig()

	zero := 0
	raw := config.RawProviderConfig{
		Type:   "openai",
		APIKey: "sk",
		Resilience: &config.RawResilienceConfig{
			CircuitBreaker: &config.RawCircuitBreakerConfig{
				FailureThreshold: &zero,
			},
		},
	}
	got := buildProviderConfig(raw, global)

	if got.Resilience.CircuitBreaker.FailureThreshold != 0 {
		t.Errorf("explicit 0 should override global, got %d", got.Resilience.CircuitBreaker.FailureThreshold)
	}
}

// --- resolveProviders (integration of all three stages) ---

func TestResolveProviders_EndToEnd(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-env")

	maxRetries := 10
	raw := map[string]config.RawProviderConfig{
		"openai": {
			Type:   "openai",
			APIKey: "sk-openai-yaml",
			Resilience: &config.RawResilienceConfig{
				Retry: &config.RawRetryConfig{MaxRetries: &maxRetries},
			},
		},
		"bad": {
			Type:   "openai",
			APIKey: "${UNRESOLVED}",
		},
	}

	got, filteredRaw := resolveProviders(raw, globalResilience, testDiscoveryConfigs)

	if _, exists := got["bad"]; exists {
		t.Error("expected provider with unresolved placeholder to be filtered out")
	}
	if got["openai"].Resilience.Retry.MaxRetries != 10 {
		t.Errorf("openai MaxRetries = %d, want 10", got["openai"].Resilience.Retry.MaxRetries)
	}
	if got["anthropic"].APIKey != "sk-ant-env" {
		t.Errorf("anthropic APIKey = %q, want sk-ant-env", got["anthropic"].APIKey)
	}
	if got["anthropic"].Resilience.Retry.MaxRetries != globalRetry.MaxRetries {
		t.Errorf("anthropic should inherit global MaxRetries=%d, got %d", globalRetry.MaxRetries, got["anthropic"].Resilience.Retry.MaxRetries)
	}
	if _, ok := filteredRaw["bad"]; ok {
		t.Error("expected filtered raw map to omit bad provider")
	}
	if filteredRaw["openai"].APIKey != "sk-openai-yaml" {
		t.Errorf("filteredRaw openai APIKey = %q", filteredRaw["openai"].APIKey)
	}
	if filteredRaw["anthropic"].APIKey != "sk-ant-env" {
		t.Errorf("filteredRaw anthropic APIKey = %q, want sk-ant-env", filteredRaw["anthropic"].APIKey)
	}
}

func TestResolveProviders_EmptyRaw_OnlyEnvVars(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "sk-groq")

	got, filteredRaw := resolveProviders(map[string]config.RawProviderConfig{}, globalResilience, testDiscoveryConfigs)

	if got["groq"].APIKey != "sk-groq" {
		t.Errorf("groq APIKey = %q, want sk-groq", got["groq"].APIKey)
	}
	if filteredRaw["groq"].APIKey != "sk-groq" {
		t.Errorf("filteredRaw groq APIKey = %q, want sk-groq", filteredRaw["groq"].APIKey)
	}
}

func TestResolveProviders_SingleCustomNamedProviderDoesNotDuplicateTypeKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-openai")

	raw := map[string]config.RawProviderConfig{
		"openai_name": {Type: "openai"},
	}

	got, filteredRaw := resolveProviders(raw, globalResilience, testDiscoveryConfigs)

	provider, exists := got["openai_name"]
	if !exists {
		t.Fatal("expected openai_name provider in resolved providers")
	}
	if provider.APIKey != "sk-openai" {
		t.Errorf("APIKey = %q, want sk-openai", provider.APIKey)
	}
	if provider.BaseURL != testDiscoveryConfigs["openai"].DefaultBaseURL {
		t.Errorf("BaseURL = %q, want %q", provider.BaseURL, testDiscoveryConfigs["openai"].DefaultBaseURL)
	}
	if _, exists := got["openai"]; exists {
		t.Fatal("expected no duplicate openai provider in resolved providers")
	}
	if _, exists := filteredRaw["openai"]; exists {
		t.Fatal("expected no duplicate openai provider in filtered raw providers")
	}
}

func TestResolveProviders_NoProvidersNoEnvVars(t *testing.T) {
	got, filteredRaw := resolveProviders(map[string]config.RawProviderConfig{}, globalResilience, testDiscoveryConfigs)
	if len(got) != 0 {
		t.Errorf("expected empty result, got %d entries", len(got))
	}
	if len(filteredRaw) != 0 {
		t.Errorf("expected empty filtered raw, got %d entries", len(filteredRaw))
	}
}

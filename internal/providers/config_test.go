package providers

import (
	"math"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	"cohere": {
		DefaultBaseURL: "https://api.cohere.com",
	},
	"gemini": {
		DefaultBaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
	},
	"vertex": {},
	"deepseek": {
		DefaultBaseURL: "https://api.deepseek.com",
	},
	"chutes": {
		DefaultBaseURL: "https://llm.chutes.ai/v1",
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
	"kilo": {
		DefaultBaseURL: "https://api.kilo.ai/api/gateway",
	},
	"zai": {
		DefaultBaseURL: "https://api.z.ai/api/paas/v4",
	},
	"vllm": {
		DefaultBaseURL:  "http://localhost:8000/v1",
		AllowAPIKeyless: true,
	},
	"llmd": {
		RequireBaseURL:  true,
		AllowAPIKeyless: true,
	},
	"sglang": {
		DefaultBaseURL:  "http://localhost:30000/v1",
		AllowAPIKeyless: true,
	},
	"azure": {
		RequireBaseURL:     true,
		SupportsAPIVersion: true,
	},
	"bedrock": {
		AllowAPIKeyless: true,
	},
	"bedrock-mantle": {
		AllowAPIKeyless: true,
	},
	"oracle": {
		RequireBaseURL: true,
	},
	"ollama": {
		DefaultBaseURL:  "http://localhost:11434/v1",
		AllowAPIKeyless: true,
	},
	"kimicode": {
		DefaultBaseURL: "https://api.kimi.com/coding/v1",
	},
	"hetzner": {
		DefaultBaseURL: "https://inference.hetzner.com/api/v1",
	},
}

// --- buildProviderConfig ---

func TestBuildProviderConfig_InheritsGlobal(t *testing.T) {
	raw := config.RawProviderConfig{Type: "openai", APIKey: "sk-test"}
	got := buildProviderConfig(raw, globalResilience)

	assert.Equal(t, "openai", got.Type)
	assert.Equal(t, globalRetry, got.Resilience.Retry)
	assert.True(t, got.SessionStickyKeys)
}

func TestBuildProviderConfig_LLMDControlDefaults(t *testing.T) {
	disabled := false
	tests := []struct {
		name          string
		raw           config.RawProviderConfig
		wantObjective string
		wantFair      bool
	}{
		{
			name:          "defaults fairness to effective user path",
			raw:           config.RawProviderConfig{Type: "llmd", InferenceObjective: "premium"},
			wantObjective: "premium",
			wantFair:      true,
		},
		{
			name:     "explicitly disables fairness derivation",
			raw:      config.RawProviderConfig{Type: "llmd", FairnessFromUserPath: &disabled},
			wantFair: false,
		},
		{
			name: "ignores llmd-only fields for another provider type",
			raw:  config.RawProviderConfig{Type: "openai", InferenceObjective: "premium"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildProviderConfig(tt.raw, globalResilience)
			assert.Equal(t, tt.wantObjective, got.InferenceObjective)
			assert.Equal(t, tt.wantFair, got.FairnessFromUserPath)
		})
	}
}

func TestBuildProviderConfig_CanDisableSessionStickyKeys(t *testing.T) {
	disabled := false
	got := buildProviderConfig(config.RawProviderConfig{
		Type:              "openai",
		APIKey:            "sk-test",
		SessionStickyKeys: &disabled,
	}, globalResilience)
	assert.False(t, got.SessionStickyKeys)
}

func TestBuildProviderConfig_NilResilience(t *testing.T) {
	raw := config.RawProviderConfig{Type: "openai", APIKey: "sk", Resilience: nil}
	got := buildProviderConfig(raw, globalResilience)

	assert.Equal(t, globalRetry, got.Resilience.Retry)
}

func TestBuildProviderConfig_NilRetry(t *testing.T) {
	raw := config.RawProviderConfig{
		Type:       "openai",
		APIKey:     "sk",
		Resilience: &config.RawResilienceConfig{Retry: nil},
	}
	got := buildProviderConfig(raw, globalResilience)

	assert.Equal(t, globalRetry, got.Resilience.Retry)
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

	assert.Equal(t, 10, got.Resilience.Retry.MaxRetries)
	assert.Equal(t, globalRetry.InitialBackoff, got.Resilience.Retry.InitialBackoff)
	assert.Equal(t, globalRetry.JitterFactor, got.Resilience.Retry.JitterFactor)
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
	assert.Equal(t, 7, r.MaxRetries)
	assert.Equal(t, 500*time.Millisecond, r.InitialBackoff)
	assert.Equal(t, 10*time.Second, r.MaxBackoff)
	assert.Equal(t, 1.5, r.BackoffFactor)
	assert.Equal(t, 0.3, r.JitterFactor)
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

	assert.Equal(t, 0, got.Resilience.Retry.MaxRetries)
}

func TestBuildProviderConfig_PreservesFields(t *testing.T) {
	raw := config.RawProviderConfig{
		Type:               "gemini",
		APIKey:             "sk-key",
		BaseURL:            "https://custom.endpoint.com",
		Backend:            "vertex",
		AuthType:           "gcp_adc",
		APIMode:            "native",
		VertexProject:      "prod-ai",
		VertexLocation:     "us-central1",
		ServiceAccountFile: "/secrets/vertex.json",
		GCPScope:           "scope-a",
		Models:             []config.RawProviderModel{{ID: "gpt-4"}, {ID: "gpt-3.5-turbo"}},
	}
	got := buildProviderConfig(raw, globalResilience)

	assert.Equal(t, "sk-key", got.APIKey)
	assert.Equal(t, "https://custom.endpoint.com", got.BaseURL)
	assert.Equal(t, "vertex", got.Backend)
	assert.Equal(t, "gcp_adc", got.AuthType)
	assert.Equal(t, "native", got.APIMode)
	assert.Equal(t, "prod-ai", got.VertexProject)
	assert.Equal(t, "us-central1", got.VertexLocation)
	assert.Equal(t, "/secrets/vertex.json", got.ServiceAccountFile)
	assert.Equal(t, "scope-a", got.GCPScope)
	assert.Len(t, got.Models, 2)
	assert.Equal(t, "gpt-4", got.Models[0])
}

func TestBuildProviderConfig_NormalizesLegacyGeminiVertexType(t *testing.T) {
	raw := config.RawProviderConfig{
		Type:           "gemini",
		Backend:        "vertex",
		AuthType:       "gcp_adc",
		VertexProject:  "prod-ai",
		VertexLocation: "us-central1",
	}

	got := buildProviderConfig(raw, globalResilience)

	require.Equal(t, "vertex", got.Type)
	require.Equal(t, "vertex", got.Backend)
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

	assert.Equal(t, 10, got["openai"].Resilience.Retry.MaxRetries)
	assert.Equal(t, globalRetry.MaxRetries, got["anthropic"].Resilience.Retry.MaxRetries)
	require.Equal(t, "openai", got["openai"].Name)
	require.Equal(t, "anthropic", got["anthropic"].Name)
}

func TestBuildProviderConfigs_EmptyMap(t *testing.T) {
	got := buildProviderConfigs(map[string]config.RawProviderConfig{}, globalResilience)
	assert.Empty(t, got)
}

// --- filterEmptyProviders ---

func TestFilterEmptyProviders_RemovesEmptyAPIKey(t *testing.T) {
	raw := map[string]config.RawProviderConfig{
		"openai":    {Type: "openai", APIKey: ""},
		"anthropic": {Type: "anthropic", APIKey: "sk-ant"},
	}
	got := filterEmptyProviders(raw, testDiscoveryConfigs)
	_, exists := got["openai"]
	assert.False(t, exists)
	_, exists = got["anthropic"]
	assert.True(t, exists)
}

func TestFilterEmptyProviders_RemovesUnresolvedPlaceholder(t *testing.T) {
	raw := map[string]config.RawProviderConfig{
		"openai":    {Type: "openai", APIKey: "${OPENAI_API_KEY}"},
		"anthropic": {Type: "anthropic", APIKey: "sk-real"},
	}
	got := filterEmptyProviders(raw, testDiscoveryConfigs)
	_, exists := got["openai"]
	assert.False(t, exists)
	_, exists = got["anthropic"]
	assert.True(t, exists)
}

func TestFilterEmptyProviders_RemovesPartialPlaceholder(t *testing.T) {
	raw := map[string]config.RawProviderConfig{
		"openai": {Type: "openai", APIKey: "prefix-${UNRESOLVED}"},
	}
	got := filterEmptyProviders(raw, testDiscoveryConfigs)
	_, exists := got["openai"]
	assert.False(t, exists)
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
			_, exists := got["ollama"]
			assert.True(t, exists, "expected ollama to be kept (%s)", tc.name)
		})
	}
}

func TestFilterEmptyProviders_VLLMAllowsKeylessConfig(t *testing.T) {
	got := filterEmptyProviders(map[string]config.RawProviderConfig{
		"vllm": {Type: "vllm", BaseURL: "http://localhost:8000/v1"},
	}, testDiscoveryConfigs)
	_, exists := got["vllm"]
	require.True(t, exists)
}

func TestFilterEmptyProvidersSGLangAllowsKeylessConfig(t *testing.T) {
	got := filterEmptyProviders(map[string]config.RawProviderConfig{
		"sglang": {Type: "sglang", BaseURL: "http://localhost:30000/v1"},
	}, testDiscoveryConfigs)
	_, exists := got["sglang"]
	require.True(t, exists)
}

func TestSkippedProviderNames_ListsDeclaredButUnresolved(t *testing.T) {
	declared := map[string]config.RawProviderConfig{
		"openai":    {Type: "openai", APIKey: "${OPENAI_API_KEY}"},
		"anthropic": {Type: "anthropic", APIKey: "sk-real"},
		"vllm-b":    {Type: "vllm"},
	}
	resolved := filterEmptyProviders(declared, testDiscoveryConfigs)

	got := skippedProviderNames(declared, resolved)
	require.Equal(t, []string{"openai"}, got)
}

func TestProviderOrigins_SplitsConfigFileFromEnv(t *testing.T) {
	// openai is declared in the config file and overlaid by env vars; it still
	// counts as coming from the file. groq exists only because of env discovery.
	declared := map[string]config.RawProviderConfig{
		"openai": {Type: "openai"},
		"vllm-b": {Type: "vllm", BaseURL: "http://b:8000/v1"},
	}
	resolved := map[string]ProviderConfig{
		"openai": {Type: "openai"},
		"vllm-b": {Type: "vllm"},
		"groq":   {Type: "groq"},
	}

	fromFile, fromEnv := providerOrigins(declared, resolved)
	require.Equal(t, []string{"openai", "vllm-b"}, fromFile)
	require.Equal(t, []string{"groq"}, fromEnv)
}

// A misindented providers: section yields no config-file providers, which is the
// signal an operator needs to see at boot.
func TestProviderOrigins_NoDeclaredProviders(t *testing.T) {
	fromFile, fromEnv := providerOrigins(nil, map[string]ProviderConfig{"openai": {Type: "openai"}})
	require.Empty(t, fromFile)
	require.Equal(t, []string{"openai"}, fromEnv)
}

func TestFilterEmptyProviders_EmptyMap(t *testing.T) {
	got := filterEmptyProviders(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)
	assert.Empty(t, got)
}

func TestFilterEmptyProviders_RemovesAzureByTypeWithoutBaseURL(t *testing.T) {
	raw := map[string]config.RawProviderConfig{
		"my-azure": {Type: "azure", APIKey: "sk-azure"},
	}

	got := filterEmptyProviders(raw, testDiscoveryConfigs)
	_, exists := got["my-azure"]
	require.False(t, exists)
}

func TestFilterEmptyProviders_RemovesOracleByTypeWithoutBaseURL(t *testing.T) {
	raw := map[string]config.RawProviderConfig{
		"oracle-primary": {Type: "oracle", APIKey: "oracle-key"},
	}

	got := filterEmptyProviders(raw, testDiscoveryConfigs)
	_, exists := got["oracle-primary"]
	require.False(t, exists)
}

func TestFilterEmptyProviders_LLMDRequiresBaseURLButNotAPIKey(t *testing.T) {
	raw := map[string]config.RawProviderConfig{
		"missing-endpoint": {Type: "llmd"},
		"router":           {Type: "llmd", BaseURL: "http://llmd-epp.default.svc/v1"},
	}

	got := filterEmptyProviders(raw, testDiscoveryConfigs)
	_, exists := got["missing-endpoint"]
	require.False(t, exists)
	_, exists = got["router"]
	require.True(t, exists)
}

// --- applyProviderEnvVars ---

func TestApplyProviderEnvVars_DiscoversFromAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-from-env")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	p, exists := got["openai"]
	require.True(t, exists)
	assert.Equal(t, "sk-from-env", p.APIKey)
	assert.Equal(t, "openai", p.Type)
}

func TestApplyProviderEnvVars_LLMDControls(t *testing.T) {
	t.Setenv("LLMD_BASE_URL", "http://llmd-epp.default.svc/v1")
	t.Setenv("LLMD_INFERENCE_OBJECTIVE", "premium-traffic")
	t.Setenv("LLMD_FAIRNESS_FROM_USER_PATH", "false")
	t.Setenv("LLMD_CANARY_BASE_URL", "http://llmd-canary.default.svc/v1")
	t.Setenv("LLMD_CANARY_INFERENCE_OBJECTIVE", "canary-traffic")
	t.Setenv("LLMD_CANARY_FAIRNESS_FROM_USER_PATH", "false")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)
	tests := []struct {
		name      string
		provider  string
		baseURL   string
		objective string
	}{
		{name: "primary", provider: "llmd", baseURL: "http://llmd-epp.default.svc/v1", objective: "premium-traffic"},
		{name: "suffixed", provider: "llmd-canary", baseURL: "http://llmd-canary.default.svc/v1", objective: "canary-traffic"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := got[tt.provider]
			assert.Equal(t, tt.baseURL, provider.BaseURL)
			assert.Equal(t, tt.objective, provider.InferenceObjective)
			require.NotNil(t, provider.FairnessFromUserPath)
			require.False(t, *provider.FairnessFromUserPath)
		})
	}
}

func TestApplyProviderEnvVars_SessionStickyKeys(t *testing.T) {
	disabled := false
	tests := []struct {
		name     string
		env      map[string]string
		raw      map[string]config.RawProviderConfig
		provider string
		want     bool
		wantNil  bool
	}{
		{
			name:     "unsuffixed false",
			env:      map[string]string{"OPENAI_API_KEY": "sk-primary", "OPENAI_SESSION_STICKY_KEYS": "false"},
			provider: "openai",
		},
		{
			name:     "suffixed true",
			env:      map[string]string{"OPENAI_EU_API_KEY": "sk-eu", "OPENAI_EU_SESSION_STICKY_KEYS": "true"},
			provider: "openai-eu",
			want:     true,
		},
		{
			name:     "environment overrides yaml",
			env:      map[string]string{"OPENAI_API_KEY": "sk-env", "OPENAI_SESSION_STICKY_KEYS": "true"},
			raw:      map[string]config.RawProviderConfig{"openai": {Type: "openai", APIKey: "sk-yaml", SessionStickyKeys: &disabled}},
			provider: "openai",
			want:     true,
		},
		{
			name:     "unset keeps default unspecified",
			env:      map[string]string{"OPENAI_API_KEY": "sk-env"},
			provider: "openai",
			wantNil:  true,
		},
		{
			name:     "invalid is ignored",
			env:      map[string]string{"OPENAI_API_KEY": "sk-env", "OPENAI_SESSION_STICKY_KEYS": "sometimes"},
			provider: "openai",
			wantNil:  true,
		},
		{
			name:     "invalid preserves yaml",
			env:      map[string]string{"OPENAI_API_KEY": "sk-env", "OPENAI_SESSION_STICKY_KEYS": "sometimes"},
			raw:      map[string]config.RawProviderConfig{"openai": {Type: "openai", APIKey: "sk-yaml", SessionStickyKeys: &disabled}},
			provider: "openai",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			got := applyProviderEnvVars(tt.raw, testDiscoveryConfigs)
			sticky := got[tt.provider].SessionStickyKeys
			if tt.wantNil {
				require.Nil(t, sticky)

				return
			}
			require.NotNil(t, sticky)
			require.Equal(t, tt.want, *sticky)
		})
	}
}

func TestApplyProviderEnvVars_DiscoversFromBaseURL(t *testing.T) {
	t.Setenv("OLLAMA_BASE_URL", "http://localhost:11434/v1")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	p, exists := got["ollama"]
	require.True(t, exists)
	assert.Equal(t, "http://localhost:11434/v1", p.BaseURL)
}

func TestApplyProviderEnvVars_DiscoversMultipleSuffixedOllamaProvidersFromBaseURLs(t *testing.T) {
	t.Setenv("OLLAMA_A_BASE_URL", "http://localhost:11434/v1")
	t.Setenv("OLLAMA_B_BASE_URL", "http://localhost:11435/v1")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	providerA, exists := got["ollama-a"]
	require.True(t, exists)
	require.Equal(t, "ollama", providerA.Type)
	require.Equal(t, "http://localhost:11434/v1", providerA.BaseURL)

	providerB, exists := got["ollama-b"]
	require.True(t, exists)
	require.Equal(t, "ollama", providerB.Type)
	require.Equal(t, "http://localhost:11435/v1", providerB.BaseURL)
}

func TestApplyProviderEnvVars_DiscoversTypeFromAPIKeyWithDefaultBaseURL(t *testing.T) {
	tests := []struct {
		providerType string
		envVar       string
		apiKey       string
	}{
		{providerType: "openrouter", envVar: "OPENROUTER_API_KEY", apiKey: "sk-openrouter"},
		{providerType: "kilo", envVar: "KILO_API_KEY", apiKey: "kilo-key"},
		{providerType: "deepseek", envVar: "DEEPSEEK_API_KEY", apiKey: "deepseek-key"},
		{providerType: "chutes", envVar: "CHUTES_API_KEY", apiKey: "cpk_test"},
		{providerType: "zai", envVar: "ZAI_API_KEY", apiKey: "zai-key"},
	}

	for _, tt := range tests {
		t.Run(tt.providerType, func(t *testing.T) {
			t.Setenv(tt.envVar, tt.apiKey)

			got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

			p, exists := got[tt.providerType]
			require.True(t, exists)
			assert.Equal(t, tt.apiKey, p.APIKey)
			assert.Equal(t, tt.providerType, p.Type)
			assert.Equal(t, testDiscoveryConfigs[tt.providerType].DefaultBaseURL, p.BaseURL)
		})
	}
}

func TestApplyProviderEnvVars_DiscoversZAIWithExplicitBaseURL(t *testing.T) {
	const explicitBaseURL = "https://api.z.ai/api/coding/paas/v4"
	t.Setenv("ZAI_API_KEY", "zai-key")
	t.Setenv("ZAI_BASE_URL", explicitBaseURL)

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	p, exists := got["zai"]
	require.True(t, exists)
	assert.Equal(t, "zai-key", p.APIKey)
	assert.Equal(t, "zai", p.Type)
	assert.Equal(t, explicitBaseURL, p.BaseURL)
}

func TestApplyProviderEnvVars_DiscoversVertexProviderFromEnvAlias(t *testing.T) {
	t.Setenv("VERTEX_PROJECT", "prod-ai")
	t.Setenv("VERTEX_LOCATION", "us-central1")
	t.Setenv("VERTEX_AUTH_TYPE", "gcp_adc")
	t.Setenv("VERTEX_API_MODE", "native")
	t.Setenv("VERTEX_MODELS", "google/gemini-2.5-flash")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	p, exists := got["vertex"]
	require.True(t, exists)
	require.Equal(t, "vertex", p.Type)
	require.Equal(t, "prod-ai", p.VertexProject)
	require.Equal(t, "us-central1", p.VertexLocation)
	require.Equal(t, "gcp_adc", p.AuthType)
	require.Equal(t, "native", p.APIMode)
	require.Len(t, p.Models, 1)
	require.Equal(t, "google/gemini-2.5-flash", p.Models[0].ID)
}

func TestApplyProviderEnvVars_DiscoversSuffixedVertexProvider(t *testing.T) {
	t.Setenv("VERTEX_US_PROJECT", "prod-ai")
	t.Setenv("VERTEX_US_LOCATION", "us-central1")
	t.Setenv("VERTEX_US_AUTH_TYPE", "gcp_service_account")
	t.Setenv("VERTEX_US_SERVICE_ACCOUNT_FILE", "/secrets/vertex.json")
	t.Setenv("BEDROCK_US_BASE_URL", "us-east-1")
	t.Setenv("BEDROCK_US_MODELS", "anthropic.claude-3-5-haiku-20241022-v1:0")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	p, exists := got["vertex-us"]
	require.True(t, exists)
	require.Equal(t, "vertex", p.Type)
	require.Equal(t, "/secrets/vertex.json", p.ServiceAccountFile)

	bedrock, exists := got["bedrock-us"]
	require.True(t, exists)
	require.Equal(t, "bedrock", bedrock.Type)
	require.Equal(t, "us-east-1", bedrock.BaseURL)
	require.Len(t, bedrock.Models, 1)
	require.Equal(t, "anthropic.claude-3-5-haiku-20241022-v1:0", bedrock.Models[0].ID)
}

func TestApplyProviderEnvVars_DiscoversBedrockMantle(t *testing.T) {
	t.Setenv("BEDROCK_MANTLE_API_KEY", "ABSK-test")
	t.Setenv("BEDROCK_MANTLE_BASE_URL", "us-east-2")
	t.Setenv("BEDROCK_MANTLE_API_MODE", "auto")
	t.Setenv("BEDROCK_MANTLE_MODELS", "openai.gpt-5.6-sol,openai.gpt-5.6-terra")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)
	p, exists := got["bedrock-mantle"]
	require.True(t, exists)
	require.Equal(t, "bedrock-mantle", p.Type)
	require.Equal(t, "ABSK-test", p.APIKey)
	require.Equal(t, "us-east-2", p.BaseURL)
	require.Equal(t, "auto", p.APIMode, "bedrock-mantle config = %+v", p)
	require.Len(t, p.Models, 2)
	require.Equal(t, "openai.gpt-5.6-sol", p.Models[0].ID)
	require.Equal(t, "openai.gpt-5.6-terra", p.Models[1].ID)
}

func TestResolveProviders_FiltersVertexWithoutProjectOrLocation(t *testing.T) {
	t.Setenv("VERTEX_PROJECT", "prod-ai")
	t.Setenv("VERTEX_AUTH_TYPE", "gcp_adc")

	got, filteredRaw := resolveProviders(map[string]config.RawProviderConfig{}, globalResilience, testDiscoveryConfigs)
	_, exists := got["vertex"]
	require.False(t, exists)
	_, exists = filteredRaw["vertex"]
	require.False(t, exists)
}

func TestResolveProviders_KeepsVertexWithProjectAndLocationUsingDefaultADC(t *testing.T) {
	t.Setenv("VERTEX_PROJECT", "prod-ai")
	t.Setenv("VERTEX_LOCATION", "us-central1")

	got, filteredRaw := resolveProviders(map[string]config.RawProviderConfig{}, globalResilience, testDiscoveryConfigs)

	p, exists := got["vertex"]
	require.True(t, exists)
	require.Equal(t, "vertex", p.Type)
	_, exists = filteredRaw["vertex"]
	require.True(t, exists)
}

func TestResolveProviders_KeepsVertexWithBaseURLWithoutProjectLocation(t *testing.T) {
	t.Setenv("VERTEX_BASE_URL", "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/publishers/google")
	t.Setenv("VERTEX_AUTH_TYPE", "gcp_adc")

	got, filteredRaw := resolveProviders(map[string]config.RawProviderConfig{}, globalResilience, testDiscoveryConfigs)

	p, exists := got["vertex"]
	require.True(t, exists)
	require.Equal(t, "vertex", p.Type)
	require.Equal(t, "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/publishers/google", p.BaseURL)
	_, exists = filteredRaw["vertex"]
	require.True(t, exists)
}

func TestResolveProviders_FiltersVertexServiceAccountWithoutCredentials(t *testing.T) {
	t.Setenv("VERTEX_PROJECT", "prod-ai")
	t.Setenv("VERTEX_LOCATION", "us-central1")
	t.Setenv("VERTEX_AUTH_TYPE", "gcp_service_account")

	got, filteredRaw := resolveProviders(map[string]config.RawProviderConfig{}, globalResilience, testDiscoveryConfigs)
	_, exists := got["vertex"]
	require.False(t, exists)
	_, exists = filteredRaw["vertex"]
	require.False(t, exists)
}

func TestResolveProviders_FiltersVertexWithUnresolvedProjectPlaceholder(t *testing.T) {
	raw := map[string]config.RawProviderConfig{
		"vertex": {
			Type:           "vertex",
			AuthType:       "gcp_adc",
			VertexProject:  "${VERTEX_PROJECT}",
			VertexLocation: "us-central1",
		},
	}

	got, filteredRaw := resolveProviders(raw, globalResilience, testDiscoveryConfigs)
	_, exists := got["vertex"]
	require.False(t, exists)
	_, exists = filteredRaw["vertex"]
	require.False(t, exists)
}

func TestResolveProviders_FiltersVertexWithUnresolvedServiceAccountPlaceholder(t *testing.T) {
	raw := map[string]config.RawProviderConfig{
		"vertex": {
			Type:               "vertex",
			AuthType:           "gcp_service_account",
			VertexProject:      "prod-ai",
			VertexLocation:     "us-central1",
			ServiceAccountFile: "${VERTEX_SERVICE_ACCOUNT_FILE}",
		},
	}

	got, filteredRaw := resolveProviders(raw, globalResilience, testDiscoveryConfigs)
	_, exists := got["vertex"]
	require.False(t, exists)
	_, exists = filteredRaw["vertex"]
	require.False(t, exists)
}

func TestApplyProviderEnvVars_GeminiIgnoresVertexSpecificEnv(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "gemini-key")
	t.Setenv("GEMINI_PROJECT", "prod-ai")
	t.Setenv("GEMINI_LOCATION", "us-central1")
	t.Setenv("GEMINI_GCP_SCOPE", "scope-a")
	t.Setenv("GEMINI_SERVICE_ACCOUNT_FILE", "/secrets/gemini.json")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	p, exists := got["gemini"]
	require.True(t, exists)
	require.Equal(t, "gemini", p.Type)
	require.Empty(t, p.VertexProject)
	require.Empty(t, p.VertexLocation)
	require.Empty(t, p.GCPScope)
	require.Empty(t, p.ServiceAccountFile)
}

func TestApplyProviderEnvVars_DiscoversVLLMFromBaseURLWithoutAPIKey(t *testing.T) {
	t.Setenv("VLLM_BASE_URL", "http://localhost:8000/v1")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	p, exists := got["vllm"]
	require.True(t, exists)
	assert.Empty(t, p.APIKey)
	assert.Equal(t, "vllm", p.Type)
	assert.Equal(t, "http://localhost:8000/v1", p.BaseURL)
}

func TestApplyProviderEnvVars_DiscoversSGLang(t *testing.T) {
	tests := []struct {
		name        string
		apiKey      string
		baseURL     string
		wantAPIKey  string
		wantBaseURL string
	}{
		{
			name:        "base URL without API key",
			baseURL:     "http://localhost:30000/v1",
			wantBaseURL: "http://localhost:30000/v1",
		},
		{
			name:        "API key with default base URL",
			apiKey:      "sglang-key",
			wantAPIKey:  "sglang-key",
			wantBaseURL: testDiscoveryConfigs["sglang"].DefaultBaseURL,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set both variables so ambient provider configuration cannot leak
			// between the keyless and authenticated discovery cases.
			t.Setenv("SGLANG_API_KEY", tt.apiKey)
			t.Setenv("SGLANG_BASE_URL", tt.baseURL)

			got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)
			p, exists := got["sglang"]
			require.True(t, exists)
			require.Equal(t, "sglang", p.Type)
			require.Equal(t, tt.wantAPIKey, p.APIKey)
			require.Equal(t, tt.wantBaseURL, p.BaseURL, "sglang config = %+v, want api_key=%q base_url=%q", p, tt.wantAPIKey, tt.wantBaseURL)
		})
	}
}

func TestApplyProviderEnvVars_DiscoversUnsuffixedAndSuffixedVLLMProvidersFromBaseURLs(t *testing.T) {
	t.Setenv("VLLM_BASE_URL", "http://localhost:8000/v1")
	t.Setenv("VLLM_TEST_BASE_URL", "http://localhost:8000/v1")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	primary, exists := got["vllm"]
	require.True(t, exists)
	require.Equal(t, "vllm", primary.Type)
	require.Equal(t, "http://localhost:8000/v1", primary.BaseURL)

	suffixed, exists := got["vllm-test"]
	require.True(t, exists)
	require.Equal(t, "vllm", suffixed.Type)
	require.Equal(t, "http://localhost:8000/v1", suffixed.BaseURL)
}

func TestApplyProviderEnvVars_DiscoversVLLMFromAPIKeyWithDefaultBaseURL(t *testing.T) {
	t.Setenv("VLLM_API_KEY", "vllm-key")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	p, exists := got["vllm"]
	require.True(t, exists)
	assert.Equal(t, "vllm-key", p.APIKey)
	assert.Equal(t, testDiscoveryConfigs["vllm"].DefaultBaseURL, p.BaseURL)
}

func TestApplyProviderEnvVars_DiscoversVLLMFromModelsEnv(t *testing.T) {
	t.Setenv("VLLM_MODELS", "meta-llama/Llama-3.1-8B-Instruct")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	p, exists := got["vllm"]
	require.True(t, exists)
	require.Equal(t, "vllm", p.Type)
	require.Len(t, p.Models, 1)
	require.Equal(t, "meta-llama/Llama-3.1-8B-Instruct", p.Models[0].ID)
}

func TestApplyProviderEnvVars_DiscoversMultipleSuffixedOpenAIProviders(t *testing.T) {
	t.Setenv("OPENAI_EAST_API_KEY", "sk-east")
	t.Setenv("OPENAI_EAST_BASE_URL", "https://east.example.com/v1")
	t.Setenv("OPENAI_WEST_API_KEY", "sk-west")
	t.Setenv("OPENAI_WEST_BASE_URL", "")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	east, exists := got["openai-east"]
	require.True(t, exists)
	assert.Equal(t, "openai", east.Type)
	assert.Equal(t, "sk-east", east.APIKey)
	assert.Equal(t, "https://east.example.com/v1", east.BaseURL)

	west, exists := got["openai-west"]
	require.True(t, exists)
	assert.Equal(t, "openai", west.Type)
	assert.Equal(t, "sk-west", west.APIKey)
	assert.Equal(t, testDiscoveryConfigs["openai"].DefaultBaseURL, west.BaseURL)
	_, exists = got["openai"]
	require.False(t, exists)
}

func TestApplyProviderEnvVars_DiscoversSuffixedProvidersForEveryRegisteredType(t *testing.T) {
	for providerType, spec := range testDiscoveryConfigs {
		prefix := envPrefix(providerType)
		t.Setenv(prefix+"_EAST_API_KEY", "key-"+providerType)
		t.Setenv(prefix+"_EAST_MODELS", "model-a-"+providerType+", model-b-"+providerType)
		if spec.RequireBaseURL {
			t.Setenv(prefix+"_EAST_BASE_URL", "https://"+providerType+".example.com/v1")
		} else {
			t.Setenv(prefix+"_EAST_BASE_URL", "")
		}
	}

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	for providerType, spec := range testDiscoveryConfigs {
		separator := spec.NameSeparator
		if separator == "" {
			separator = "-"
		}
		name := providerType + separator + "east"
		p, exists := got[name]
		require.True(t, exists, "expected %s to be discovered from suffixed env vars", name)
		assert.Equal(t, providerType, p.Type, "%s Type", name)
		assert.Equal(t, "key-"+providerType, p.APIKey, "%s APIKey", name)

		if spec.RequireBaseURL {
			assert.Equal(t, "https://"+providerType+".example.com/v1", p.BaseURL, "%s BaseURL", name)
		} else if spec.DefaultBaseURL != "" {
			assert.Equal(t, spec.DefaultBaseURL, p.BaseURL, "%s BaseURL", name)
		}
		assert.Len(t, p.Models, 2)
		assert.Equal(t, "model-a-"+providerType, p.Models[0].ID)
		assert.Equal(t, "model-b-"+providerType, p.Models[1].ID, "%s Models", name)
	}
}

func TestApplyProviderEnvVars_DiscoversAzureFromExplicitEnvVars(t *testing.T) {
	t.Setenv("AZURE_API_KEY", "sk-azure")
	t.Setenv("AZURE_BASE_URL", "https://example-resource.openai.azure.com/openai/deployments/gpt-4o")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	p, exists := got["azure"]
	require.True(t, exists)
	assert.Equal(t, "sk-azure", p.APIKey)
	assert.Equal(t, "azure", p.Type)
	assert.Equal(t, "https://example-resource.openai.azure.com/openai/deployments/gpt-4o", p.BaseURL)
}

func TestApplyProviderEnvVars_AzureAPIVersionEnvWins(t *testing.T) {
	t.Setenv("AZURE_API_KEY", "sk-azure")
	t.Setenv("AZURE_BASE_URL", "https://example-resource.openai.azure.com/openai/deployments/gpt-4o")
	t.Setenv("AZURE_API_VERSION", "2025-04-01-preview")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	p, exists := got["azure"]
	require.True(t, exists)
	assert.Equal(t, "2025-04-01-preview", p.APIVersion)
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

	require.Equal(t, "2025-04-01-preview", got["azure"].APIVersion)
}

func TestApplyProviderEnvVars_DoesNotDiscoverAzureWithoutBaseURL(t *testing.T) {
	t.Setenv("AZURE_API_KEY", "sk-azure")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)
	_, exists := got["azure"]
	require.False(t, exists)
}

func TestApplyProviderEnvVars_DiscoversSuffixedAzureWithAPIVersion(t *testing.T) {
	t.Setenv("AZURE_GPT4O_API_KEY", "sk-azure")
	t.Setenv("AZURE_GPT4O_BASE_URL", "https://example-resource.openai.azure.com/openai/deployments/gpt-4o")
	t.Setenv("AZURE_GPT4O_API_VERSION", "2025-04-01-preview")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	p, exists := got["azure-gpt4o"]
	require.True(t, exists)
	assert.Equal(t, "azure", p.Type)
	assert.Equal(t, "sk-azure", p.APIKey)
	assert.Equal(t, "https://example-resource.openai.azure.com/openai/deployments/gpt-4o", p.BaseURL)
	assert.Equal(t, "2025-04-01-preview", p.APIVersion)
}

func TestApplyProviderEnvVars_DoesNotDiscoverSuffixedAzureWithoutBaseURL(t *testing.T) {
	t.Setenv("AZURE_EAST_API_KEY", "sk-azure")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)
	_, exists := got["azure-east"]
	require.False(t, exists)
}

func TestApplyProviderEnvVars_DiscoversOracleFromExplicitEnvVars(t *testing.T) {
	t.Setenv("ORACLE_API_KEY", "oracle-key")
	t.Setenv("ORACLE_BASE_URL", "https://inference.generativeai.us-chicago-1.oci.oraclecloud.com/20231130/actions/v1")
	t.Setenv("ORACLE_MODELS", " openai.gpt-oss-120b, xai.grok-3 ,, ")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	p, exists := got["oracle"]
	require.True(t, exists)
	assert.Equal(t, "oracle-key", p.APIKey)
	assert.Equal(t, "oracle", p.Type)
	assert.Equal(t, "https://inference.generativeai.us-chicago-1.oci.oraclecloud.com/20231130/actions/v1", p.BaseURL)
	assert.Len(t, p.Models, 2)
	assert.Equal(t, "openai.gpt-oss-120b", p.Models[0].ID)
	assert.Equal(t, "xai.grok-3", p.Models[1].ID)
}

func TestApplyProviderEnvVars_DiscoversSuffixedOracleModels(t *testing.T) {
	t.Setenv("ORACLE_REGION_API_KEY", "oracle-key")
	t.Setenv("ORACLE_REGION_BASE_URL", "https://oracle.example.com/v1")
	t.Setenv("ORACLE_REGION_MODELS", " openai.gpt-oss-120b, xai.grok-3 ,, ")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	p, exists := got["oracle-region"]
	require.True(t, exists)
	assert.Equal(t, "oracle", p.Type)
	assert.Equal(t, "oracle-key", p.APIKey)
	assert.Equal(t, "https://oracle.example.com/v1", p.BaseURL)
	assert.Len(t, p.Models, 2)
	assert.Equal(t, "openai.gpt-oss-120b", p.Models[0].ID)
	assert.Equal(t, "xai.grok-3", p.Models[1].ID)
}

func TestApplyProviderEnvVars_DoesNotDiscoverOracleWithoutBaseURL(t *testing.T) {
	t.Setenv("ORACLE_API_KEY", "oracle-key")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)
	_, exists := got["oracle"]
	require.False(t, exists)
}

func TestApplyProviderEnvVars_OracleModelsEnvWinsOverYAMLWithoutOtherOracleEnvVars(t *testing.T) {
	raw := map[string]config.RawProviderConfig{
		"oracle": {
			Type:    "oracle",
			APIKey:  "oracle-key",
			BaseURL: "https://inference.generativeai.us-chicago-1.oci.oraclecloud.com/20231130/actions/v1",
			Models:  []config.RawProviderModel{{ID: "yaml-model"}},
		},
	}
	t.Setenv("ORACLE_MODELS", "openai.gpt-oss-120b, xai.grok-3")

	got := applyProviderEnvVars(raw, testDiscoveryConfigs)

	p := got["oracle"]
	require.Equal(t, "oracle-key", p.APIKey)
	require.Equal(t, "https://inference.generativeai.us-chicago-1.oci.oraclecloud.com/20231130/actions/v1", p.BaseURL)
	require.Len(t, p.Models, 2)
	require.Equal(t, "openai.gpt-oss-120b", p.Models[0].ID)
	require.Equal(t, "xai.grok-3", p.Models[1].ID)
}

func TestApplyProviderEnvVars_EnvWinsOverYAML(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-env-key")

	raw := map[string]config.RawProviderConfig{
		"openai": {Type: "openai", APIKey: "sk-yaml-key", BaseURL: "https://custom.api.com"},
	}
	got := applyProviderEnvVars(raw, testDiscoveryConfigs)

	assert.Equal(t, "sk-env-key", got["openai"].APIKey)
	assert.Equal(t, "https://custom.api.com", got["openai"].BaseURL)
}

func TestApplyProviderEnvVars_SingleCustomNamedProviderUsesTypeEnvVars(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-env-key")

	raw := map[string]config.RawProviderConfig{
		"openai_name": {Type: "openai"},
	}
	got := applyProviderEnvVars(raw, testDiscoveryConfigs)

	provider, exists := got["openai_name"]
	require.True(t, exists)
	assert.Equal(t, "sk-env-key", provider.APIKey)
	assert.Equal(t, testDiscoveryConfigs["openai"].DefaultBaseURL, provider.BaseURL)
	_, exists = got["openai"]
	require.False(t, exists)
}

func TestApplyProviderEnvVars_AmbiguousCustomNamedProvidersSkipTypeEnvOverlay(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-env-key")

	raw := map[string]config.RawProviderConfig{
		"openai-east": {Type: "openai", APIKey: "east-key", BaseURL: "https://east.example.com/v1"},
		"openai-west": {Type: "openai", APIKey: "west-key", BaseURL: "https://west.example.com/v1"},
	}
	got := applyProviderEnvVars(raw, testDiscoveryConfigs)

	assert.Equal(t, "east-key", got["openai-east"].APIKey)
	assert.Equal(t, "west-key", got["openai-west"].APIKey)
	_, exists := got["openai"]
	require.False(t, exists)
}

func TestApplyProviderEnvVars_BaseURLEnvWinsOverYAML(t *testing.T) {
	t.Setenv("OPENAI_BASE_URL", "https://env-override.com")

	raw := map[string]config.RawProviderConfig{
		"openai": {Type: "openai", APIKey: "sk-key", BaseURL: "https://yaml-url.com"},
	}
	got := applyProviderEnvVars(raw, testDiscoveryConfigs)

	assert.Equal(t, "https://env-override.com", got["openai"].BaseURL)
}

func TestApplyProviderEnvVars_DefaultBaseReplacesPlaceholderYAMLBaseURL(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-openrouter")

	raw := map[string]config.RawProviderConfig{
		"openrouter": {Type: "openrouter", APIKey: "sk-yaml", BaseURL: "${OPENROUTER_BASE_URL}"},
	}

	got := applyProviderEnvVars(raw, testDiscoveryConfigs)

	require.Equal(t, testDiscoveryConfigs["openrouter"].DefaultBaseURL, got["openrouter"].BaseURL)
}

func TestApplyProviderEnvVars_PlaceholderBaseURLEnvFallsBackToDefault(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-openrouter")
	t.Setenv("OPENROUTER_BASE_URL", "${OPENROUTER_BASE_URL}")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	require.Equal(t, testDiscoveryConfigs["openrouter"].DefaultBaseURL, got["openrouter"].BaseURL)
}

func TestApplyProviderEnvVars_DoesNotDiscoverAzureWithPlaceholderBaseURL(t *testing.T) {
	t.Setenv("AZURE_API_KEY", "sk-azure")
	t.Setenv("AZURE_BASE_URL", "${AZURE_BASE_URL}")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)
	_, exists := got["azure"]
	require.False(t, exists)
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

	require.NotNil(t, got["openai"].Resilience)
	require.NotNil(t, got["openai"].Resilience.Retry)
	assert.Equal(t, 10, *got["openai"].Resilience.Retry.MaxRetries)
}

func TestApplyProviderEnvVars_SuffixedEnvOverlaysMatchingYAMLProvider(t *testing.T) {
	t.Setenv("OPENAI_EAST_API_KEY", "sk-env-key")
	t.Setenv("OPENAI_EAST_BASE_URL", "https://env.example.com/v1")

	maxRetries := 10
	raw := map[string]config.RawProviderConfig{
		"openai-east": {
			Type:    "openai",
			APIKey:  "sk-yaml-key",
			BaseURL: "https://yaml.example.com/v1",
			Resilience: &config.RawResilienceConfig{
				Retry: &config.RawRetryConfig{MaxRetries: &maxRetries},
			},
		},
	}

	got := applyProviderEnvVars(raw, testDiscoveryConfigs)

	p := got["openai-east"]
	assert.Equal(t, "sk-env-key", p.APIKey)
	assert.Equal(t, "https://env.example.com/v1", p.BaseURL)
	require.NotNil(t, p.Resilience)
	require.NotNil(t, p.Resilience.Retry)
	assert.Equal(t, 10, *p.Resilience.Retry.MaxRetries)
}

// providerEnvNames mirrors the env-var naming convention applied by
// applyProviderEnvVars, so tests can clear ambient variables per provider.
type providerEnvNames struct {
	APIKey     string
	BaseURL    string
	APIVersion string
	Models     string
}

func derivedEnvNames(providerType string) providerEnvNames {
	prefix := envPrefix(providerType)
	return providerEnvNames{
		APIKey:     prefix + "_API_KEY",
		BaseURL:    prefix + "_BASE_URL",
		APIVersion: prefix + "_API_VERSION",
		Models:     prefix + "_MODELS",
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
	assert.Empty(t, got)
}

func TestApplyProviderEnvVars_PreservesUnknownYAMLProviders(t *testing.T) {
	raw := map[string]config.RawProviderConfig{
		"custom-provider": {Type: "custom", APIKey: "sk-custom"},
	}
	got := applyProviderEnvVars(raw, testDiscoveryConfigs)
	_, exists := got["custom-provider"]
	assert.True(t, exists)
}

// --- buildProviderConfig: circuit breaker ---

func TestBuildProviderConfig_CircuitBreaker_InheritsGlobal(t *testing.T) {
	global := globalResilience
	global.CircuitBreaker = config.CircuitBreakerConfig{
		Scope:            "provider",
		FailureThreshold: 5,
		SuccessThreshold: 2,
		Timeout:          30 * time.Second,
	}
	raw := config.RawProviderConfig{Type: "openai", APIKey: "sk"}
	got := buildProviderConfig(raw, global)

	assert.Equal(t, global.CircuitBreaker, got.Resilience.CircuitBreaker)
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

	assert.Equal(t, global.CircuitBreaker, got.Resilience.CircuitBreaker)
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

	assert.Equal(t, 10, got.Resilience.CircuitBreaker.FailureThreshold)
	assert.Equal(t, global.CircuitBreaker.SuccessThreshold, got.Resilience.CircuitBreaker.SuccessThreshold)
	assert.Equal(t, global.CircuitBreaker.Timeout, got.Resilience.CircuitBreaker.Timeout)
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
	assert.Equal(t, 3, cb.FailureThreshold)
	assert.Equal(t, 1, cb.SuccessThreshold)
	assert.Equal(t, 10*time.Second, cb.Timeout)
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

	assert.Equal(t, 0, got.Resilience.CircuitBreaker.FailureThreshold)
}

func TestBuildProviderConfig_CircuitBreaker_EnabledOverride(t *testing.T) {
	global := globalResilience
	global.CircuitBreaker = config.DefaultCircuitBreakerConfig()

	disabled := false
	raw := config.RawProviderConfig{
		Type:   "openai",
		APIKey: "sk",
		Resilience: &config.RawResilienceConfig{
			CircuitBreaker: &config.RawCircuitBreakerConfig{
				Enabled: &disabled,
			},
		},
	}
	got := buildProviderConfig(raw, global)

	assert.False(t, got.Resilience.CircuitBreaker.Enabled)
	assert.Equal(t, global.CircuitBreaker.FailureThreshold, got.Resilience.CircuitBreaker.FailureThreshold)

	// A globally disabled breaker can be re-enabled for one provider.
	global.CircuitBreaker.Enabled = false
	enabled := true
	raw.Resilience.CircuitBreaker.Enabled = &enabled
	got = buildProviderConfig(raw, global)
	assert.True(t, got.Resilience.CircuitBreaker.Enabled)
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
	_, exists := got["bad"]
	assert.False(t, exists)
	assert.Equal(t, 10, got["openai"].Resilience.Retry.MaxRetries)
	assert.Equal(t, "sk-ant-env", got["anthropic"].APIKey)
	assert.Equal(t, globalRetry.MaxRetries, got["anthropic"].Resilience.Retry.MaxRetries)
	_, ok := filteredRaw["bad"]
	assert.False(t, ok)
	assert.Equal(t, "sk-openai-yaml", filteredRaw["openai"].APIKey)
	assert.Equal(t, "sk-ant-env", filteredRaw["anthropic"].APIKey)
}

func TestResolveProviders_EmptyRaw_OnlyEnvVars(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "sk-groq")

	got, filteredRaw := resolveProviders(map[string]config.RawProviderConfig{}, globalResilience, testDiscoveryConfigs)

	assert.Equal(t, "sk-groq", got["groq"].APIKey)
	assert.Equal(t, "sk-groq", filteredRaw["groq"].APIKey)
}

func TestResolveProviders_EmptyRaw_SuffixedEnvVars(t *testing.T) {
	t.Setenv("OPENAI_EAST_API_KEY", "sk-east")
	t.Setenv("OPENAI_WEST_API_KEY", "sk-west")
	t.Setenv("OPENAI_WEST_BASE_URL", "https://west.example.com/v1")

	got, filteredRaw := resolveProviders(map[string]config.RawProviderConfig{}, globalResilience, testDiscoveryConfigs)

	east, exists := got["openai-east"]
	require.True(t, exists)
	assert.Equal(t, "openai", east.Type)
	assert.Equal(t, "sk-east", east.APIKey)
	assert.Equal(t, testDiscoveryConfigs["openai"].DefaultBaseURL, east.BaseURL)

	west, exists := got["openai-west"]
	require.True(t, exists)
	assert.Equal(t, "openai", west.Type)
	assert.Equal(t, "sk-west", west.APIKey)
	assert.Equal(t, "https://west.example.com/v1", west.BaseURL)
	assert.Equal(t, "sk-east", filteredRaw["openai-east"].APIKey)
	assert.Equal(t, "https://west.example.com/v1", filteredRaw["openai-west"].BaseURL)
	_, exists = got["openai"]
	require.False(t, exists)
}

func TestResolveProviders_SingleCustomNamedProviderDoesNotDuplicateTypeKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-openai")

	raw := map[string]config.RawProviderConfig{
		"openai_name": {Type: "openai"},
	}

	got, filteredRaw := resolveProviders(raw, globalResilience, testDiscoveryConfigs)

	provider, exists := got["openai_name"]
	require.True(t, exists)
	assert.Equal(t, "sk-openai", provider.APIKey)
	assert.Equal(t, testDiscoveryConfigs["openai"].DefaultBaseURL, provider.BaseURL)
	_, exists = got["openai"]
	require.False(t, exists)
	_, exists = filteredRaw["openai"]
	require.False(t, exists)
}

func TestResolveProviders_NoProvidersNoEnvVars(t *testing.T) {
	got, filteredRaw := resolveProviders(map[string]config.RawProviderConfig{}, globalResilience, testDiscoveryConfigs)
	assert.Empty(t, got)
	assert.Empty(t, filteredRaw)
}

func TestBuildProviderConfig_Hetzner_ResolvesBaseURL(t *testing.T) {
	t.Setenv("HETZNER_API_KEY", "hetzner-test-key")

	raw := map[string]config.RawProviderConfig{
		"hetzner": {Type: "hetzner", APIKey: "hetzner-test-key"},
	}
	got := applyProviderEnvVars(raw, testDiscoveryConfigs)

	p, exists := got["hetzner"]
	require.True(t, exists)
	assert.Equal(t, "hetzner", p.Type)
	assert.Equal(t, "hetzner-test-key", p.APIKey)
	assert.Equal(t, testDiscoveryConfigs["hetzner"].DefaultBaseURL, p.BaseURL)
}

func TestApplyProviderEnvVars_ModelFilter(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-openrouter")
	t.Setenv("OPENROUTER_MODEL_FILTER_INCLUDE", "*:free, *:nitro")
	t.Setenv("OPENROUTER_MODEL_FILTER_EXCLUDE", "*-preview:free")
	t.Setenv("OPENROUTER_MODEL_FILTER_MAX_PRICE_PER_MTOK", "0")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	p, exists := got["openrouter"]
	require.True(t, exists)
	assert.Equal(t, []string{"*:free", "*:nitro"}, p.ModelFilter.Include)
	assert.Equal(t, []string{"*-preview:free"}, p.ModelFilter.Exclude)
	assert.NotNil(t, p.ModelFilter.MaxPricePerMtok)
	assert.Equal(t, float64(0), *p.ModelFilter.MaxPricePerMtok)
}

// Each filter rule overlays independently so an env price cap can narrow a YAML
// pattern filter without restating it.
func TestApplyProviderEnvVars_ModelFilterOverlaysYAMLPerRule(t *testing.T) {
	t.Setenv("OPENROUTER_MODEL_FILTER_MAX_PRICE_PER_MTOK", "0.5")

	raw := map[string]config.RawProviderConfig{
		"openrouter": {
			Type:        "openrouter",
			APIKey:      "sk-yaml",
			ModelFilter: config.ModelFilter{Include: []string{"qwen/*"}},
		},
	}
	got := applyProviderEnvVars(raw, testDiscoveryConfigs)

	filter := got["openrouter"].ModelFilter
	assert.Equal(t, []string{"qwen/*"}, filter.Include, "Include preserved from YAML")
	assert.NotNil(t, filter.MaxPricePerMtok)
	assert.Equal(t, 0.5, *filter.MaxPricePerMtok)
}

// A malformed cap must not resolve to a silent zero (hiding every paid model)
// nor to an absent cap (routing above the operator's intended limit). It is
// carried through as NaN so startup validation rejects it.
func TestApplyProviderEnvVars_ModelFilterRejectsMalformedPrice(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-openrouter")
	t.Setenv("OPENROUTER_MODEL_FILTER_MAX_PRICE_PER_MTOK", "cheap")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, testDiscoveryConfigs)

	limit := got["openrouter"].ModelFilter.MaxPricePerMtok
	require.NotNil(t, limit)
	require.True(t, math.IsNaN(*limit))
	assert.Error(t, got["openrouter"].ModelFilter.Validate("providers.openrouter.model_filter"))
}

// The malformed value must survive the whole resolution path, not just the env
// parser: a cost cap that vanishes between parsing and validation is worse than
// no cap, because the operator believes one is in force.
func TestResolveProviders_RejectsMalformedModelFilterPrice(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-openrouter")
	t.Setenv("OPENROUTER_MODEL_FILTER_MAX_PRICE_PER_MTOK", "cheap")

	resolved, _ := resolveProviders(map[string]config.RawProviderConfig{}, globalResilience, testDiscoveryConfigs)
	_, ok := resolved["openrouter"]
	require.True(t, ok)

	err := validateProviderModelFilters(resolved)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "providers.openrouter.model_filter.max_price_per_mtok")
}

func TestBuildProviderConfig_NormalizesModelFilter(t *testing.T) {
	resolved := buildProviderConfig(config.RawProviderConfig{
		Type:        "openrouter",
		APIKey:      "sk-openrouter",
		ModelFilter: config.ModelFilter{Include: []string{" *:free ", "", "  "}},
	}, globalResilience)
	assert.Equal(t, []string{"*:free"}, resolved.ModelFilter.Include)
}

// A price cap that parses but cannot express a real limit must fail startup:
// NaN rejects every model, +Inf disables the cap, and a negative cap can never
// be met. Validation runs after the env overlay, so it covers both sources.
func TestValidateProviderModelFilters(t *testing.T) {
	tests := []struct {
		name    string
		filter  config.ModelFilter
		wantErr bool
	}{
		{name: "no filter"},
		{name: "patterns only", filter: config.ModelFilter{Include: []string{"*:free"}}},
		{name: "zero cap", filter: config.ModelFilter{MaxPricePerMtok: new(0.0)}},
		{name: "negative cap", filter: config.ModelFilter{MaxPricePerMtok: new(-1.0)}, wantErr: true},
		{name: "NaN cap", filter: config.ModelFilter{MaxPricePerMtok: new(math.NaN())}, wantErr: true},
		{name: "infinite cap", filter: config.ModelFilter{MaxPricePerMtok: new(math.Inf(1))}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateProviderModelFilters(map[string]ProviderConfig{
				"openrouter": {Type: "openrouter", ModelFilter: tt.filter},
			})
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, "providers.openrouter.model_filter", "error should name the offending provider")
		})
	}
}

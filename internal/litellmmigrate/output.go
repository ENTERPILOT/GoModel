package litellmmigrate

import "github.com/enterpilot/gomodel/internal/core"

// The types below mirror the subset of GoModel's config.yaml the converter
// writes. They carry omitempty everywhere so the generated file lists only
// what the LiteLLM config actually set; everything else keeps GoModel's
// defaults.

type gomodelConfig struct {
	Server        *serverOut              `yaml:"server,omitempty"`
	Models        *modelsOut              `yaml:"models,omitempty"`
	HTTP          *httpOut                `yaml:"http,omitempty"`
	Resilience    *resilienceOut          `yaml:"resilience,omitempty"`
	RateLimits    *rateLimitsOut          `yaml:"rate_limits,omitempty"`
	Metrics       *enabledOut             `yaml:"metrics,omitempty"`
	OpenTelemetry *enabledOut             `yaml:"opentelemetry,omitempty"`
	Providers     map[string]*providerOut `yaml:"providers,omitempty"`
	VirtualModels []virtualModelOut       `yaml:"virtual_models,omitempty"`
}

type serverOut struct {
	MasterKey string `yaml:"master_key,omitempty"`
}

type modelsOut struct {
	KeepOnlyAliasesAtModelsEndpoint bool   `yaml:"keep_only_aliases_at_models_endpoint,omitempty"`
	ConfiguredProviderModelsMode    string `yaml:"configured_provider_models_mode,omitempty"`
}

type httpOut struct {
	Timeout int `yaml:"timeout,omitempty"`
}

type resilienceOut struct {
	Retry          *retryOut          `yaml:"retry,omitempty"`
	CircuitBreaker *circuitBreakerOut `yaml:"circuit_breaker,omitempty"`
}

type retryOut struct {
	MaxRetries *int `yaml:"max_retries,omitempty"`
}

type circuitBreakerOut struct {
	FailureThreshold int    `yaml:"failure_threshold,omitempty"`
	Timeout          string `yaml:"timeout,omitempty"`
}

type rateLimitsOut struct {
	Models []modelRateLimitOut `yaml:"models,omitempty"`
}

type modelRateLimitOut struct {
	Model  string         `yaml:"model"`
	Limits []rateLimitOut `yaml:"limits"`
}

type rateLimitOut struct {
	Period      string `yaml:"period"`
	MaxRequests int64  `yaml:"max_requests,omitempty"`
	MaxTokens   int64  `yaml:"max_tokens,omitempty"`
}

type enabledOut struct {
	Enabled bool `yaml:"enabled"`
}

type providerOut struct {
	Type               string          `yaml:"type"`
	APIKey             string          `yaml:"api_key,omitempty"`
	BaseURL            string          `yaml:"base_url,omitempty"`
	APIVersion         string          `yaml:"api_version,omitempty"`
	AuthType           string          `yaml:"auth_type,omitempty"`
	VertexProject      string          `yaml:"vertex_project,omitempty"`
	VertexLocation     string          `yaml:"vertex_location,omitempty"`
	ServiceAccountFile string          `yaml:"service_account_file,omitempty"`
	ServiceAccountJSON string          `yaml:"service_account_json,omitempty"`
	Models             []providerModel `yaml:"models,omitempty"`
	ModelFilter        *modelFilterOut `yaml:"model_filter,omitempty"`
}

type providerModel struct {
	ID       string              `yaml:"id"`
	Metadata *core.ModelMetadata `yaml:"metadata,omitempty"`
}

type modelFilterOut struct {
	Include []string `yaml:"include,omitempty"`
}

type virtualModelOut struct {
	Source      string      `yaml:"source"`
	Strategy    string      `yaml:"strategy,omitempty"`
	Targets     []targetOut `yaml:"targets"`
	Description string      `yaml:"description,omitempty"`
}

type targetOut struct {
	Provider string  `yaml:"provider,omitempty"`
	Model    string  `yaml:"model"`
	Weight   float64 `yaml:"weight,omitempty"`
}

func (t targetOut) qualified() string {
	if t.Provider == "" {
		return t.Model
	}
	return t.Provider + "/" + t.Model
}

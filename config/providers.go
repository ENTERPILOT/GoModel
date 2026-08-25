package config

// RawProviderConfig is the YAML-sourced provider configuration before env var
// overrides, credential filtering, or resilience merging. Exported so the
// providers package can resolve it into a fully-configured ProviderConfig.
type RawProviderConfig struct {
	Type   string `yaml:"type"`
	APIKey string `yaml:"api_key"`
	// APIKeys lists additional API keys for this provider. When more than one
	// key is resolved (counting APIKey), identified sessions stay on one key by
	// default while sessionless requests rotate round robin. Set it via
	// `api_keys:` or the `<PROVIDER>_API_KEY_<n>` env vars.
	APIKeys []string `yaml:"api_keys"`
	// SessionStickyKeys defaults to true. Set false to restore round-robin key
	// selection for every request, including requests carrying a session ID.
	SessionStickyKeys        *bool  `yaml:"session_sticky_keys"`
	BaseURL                  string `yaml:"base_url"`
	APIVersion               string `yaml:"api_version"`
	Backend                  string `yaml:"backend"`
	AuthType                 string `yaml:"auth_type"`
	APIMode                  string `yaml:"api_mode"`
	VertexProject            string `yaml:"vertex_project"`
	VertexLocation           string `yaml:"vertex_location"`
	ServiceAccountFile       string `yaml:"service_account_file"`
	ServiceAccountJSON       string `yaml:"service_account_json"`
	ServiceAccountJSONBase64 string `yaml:"service_account_json_base64"`
	GCPScope                 string `yaml:"gcp_scope"`
	// InferenceObjective is the trusted llm-d InferenceObjective name injected
	// into outbound requests. It is ignored by provider types other than llmd.
	InferenceObjective string `yaml:"inference_objective"`
	// FairnessFromUserPath controls whether the llmd provider derives its
	// fairness ID from GoModel's effective (authenticated) user path. It
	// defaults to true; nil preserves that default.
	FairnessFromUserPath *bool              `yaml:"fairness_from_user_path"`
	Models               []RawProviderModel `yaml:"models"`
	// ModelFilter narrows the provider's model inventory to the models that
	// match its patterns and price cap. It applies to the final inventory, so
	// it also narrows models added by `models` in merge or allowlist mode.
	ModelFilter ModelFilter          `yaml:"model_filter"`
	Resilience  *RawResilienceConfig `yaml:"resilience"`
}

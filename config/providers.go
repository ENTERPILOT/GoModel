package config

// RawProviderConfig is the YAML-sourced provider configuration before env var
// overrides, credential filtering, or resilience merging. Exported so the
// providers package can resolve it into a fully-configured ProviderConfig.
type RawProviderConfig struct {
	Type   string `yaml:"type"`
	APIKey string `yaml:"api_key"`
	// APIKeys lists additional API keys for this provider. When more than one
	// key is resolved (counting APIKey), requests rotate across them round
	// robin. Set it via `api_keys:` or the `<PROVIDER>_API_KEY_<n>` env vars.
	APIKeys                  []string             `yaml:"api_keys"`
	BaseURL                  string               `yaml:"base_url"`
	APIVersion               string               `yaml:"api_version"`
	Backend                  string               `yaml:"backend"`
	AuthType                 string               `yaml:"auth_type"`
	APIMode                  string               `yaml:"api_mode"`
	VertexProject            string               `yaml:"vertex_project"`
	VertexLocation           string               `yaml:"vertex_location"`
	ServiceAccountFile       string               `yaml:"service_account_file"`
	ServiceAccountJSON       string               `yaml:"service_account_json"`
	ServiceAccountJSONBase64 string               `yaml:"service_account_json_base64"`
	GCPScope                 string               `yaml:"gcp_scope"`
	Models                   []RawProviderModel   `yaml:"models"`
	Resilience               *RawResilienceConfig `yaml:"resilience"`
}

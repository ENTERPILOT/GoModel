package config

// RawProviderConfig is the YAML-sourced provider configuration before env var
// overrides, credential filtering, or resilience merging. Exported so the
// providers package can resolve it into a fully-configured ProviderConfig.
type RawProviderConfig struct {
	Type                     string               `yaml:"type"`
	APIKey                   string               `yaml:"api_key"`
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
	// CustomUpstreamHeaders are extra HTTP headers the gateway attaches to every
	// outbound request to this provider, on top of the auth/transport defaults.
	CustomUpstreamHeaders    map[string]string    `yaml:"custom_upstream_headers"`
	// PassthroughUserHeaders enables forwarding a curated set of client-supplied
	// request headers to the upstream provider.
	PassthroughUserHeaders   bool                 `yaml:"passthrough_user_headers"`
	// PassthroughUserHeadersSkip lists header names to exclude from passthrough.
	// Interpretation depends on PassthroughUserHeadersSkipMode.
	PassthroughUserHeadersSkip      []string `yaml:"passthrough_user_headers_skip"`
	// PassthroughUserHeadersSkipMode controls how the skip list is applied:
	// "skip" (default) removes the listed headers, "only" keeps only the
	// listed headers and drops everything else passthrough would forward.
	PassthroughUserHeadersSkipMode  string   `yaml:"passthrough_user_headers_skip_mode"`
	Resilience               *RawResilienceConfig `yaml:"resilience"`
}

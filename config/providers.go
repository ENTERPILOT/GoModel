package config

// RawProviderConfig is the YAML-sourced provider configuration before env var
// overrides, credential filtering, or resilience merging. Exported so the
// providers package can resolve it into a fully-configured ProviderConfig.
type RawProviderConfig struct {
	Type   string `yaml:"type"`
	APIKey string `yaml:"api_key" export:"secret"`
	// APIKeys lists additional API keys for this provider. When more than one
	// key is resolved (counting APIKey), identified sessions stay on one key by
	// default while sessionless requests rotate round robin. Set it via
	// `api_keys:` or the `<PROVIDER>_API_KEY_<n>` env vars.
	APIKeys []string `yaml:"api_keys" export:"secret"`
	// SessionStickyKeys defaults to true. Set false to restore round-robin key
	// selection for every request, including requests carrying a session ID.
	SessionStickyKeys        *bool  `yaml:"session_sticky_keys"`
	BaseURL                  string `yaml:"base_url" export:"url"`
	APIVersion               string `yaml:"api_version"`
	Backend                  string `yaml:"backend"`
	AuthType                 string `yaml:"auth_type"`
	APIMode                  string `yaml:"api_mode"`
	VertexProject            string `yaml:"vertex_project"`
	VertexLocation           string `yaml:"vertex_location"`
	ServiceAccountFile       string `yaml:"service_account_file"`
	ServiceAccountJSON       string `yaml:"service_account_json" export:"secret"`
	ServiceAccountJSONBase64 string `yaml:"service_account_json_base64" export:"secret"`
	GCPScope                 string `yaml:"gcp_scope"`
	// ProxyURL sends this provider's upstream traffic through an HTTP, HTTPS,
	// or SOCKS5 proxy ("http://proxy:3128", "socks5://user:pass@host:1080").
	// Empty keeps the process-wide HTTP_PROXY / HTTPS_PROXY / NO_PROXY
	// behaviour. Set it via `proxy_url:` or `<PROVIDER>_PROXY_URL`.
	ProxyURL string `yaml:"proxy_url" export:"url"`
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
	// WorkloadIdentity authenticates with a short-lived token exchanged for
	// the workload's platform identity token instead of a static API key.
	// Supported by the openai and anthropic types. An API key, when also set,
	// wins, matching the official SDKs.
	WorkloadIdentity WorkloadIdentityConfig `yaml:"workload_identity"`
}

// WorkloadIdentityConfig configures workload identity federation: the gateway
// reads an identity token issued by its platform (Kubernetes, AWS, GCP,
// Azure, GitHub Actions, SPIFFE) and exchanges it for a short-lived provider
// access token. Which fields are required depends on the provider type.
type WorkloadIdentityConfig struct {
	// IdentityTokenFile is re-read on every exchange, so projected tokens
	// that rotate on disk stay current. It wins over IdentityToken.
	IdentityTokenFile string `yaml:"identity_token_file"`
	IdentityToken     string `yaml:"identity_token" export:"secret"`
	// TokenEndpoint overrides the provider's default exchange endpoint.
	TokenEndpoint    string `yaml:"token_endpoint" export:"url"`
	ServiceAccountID string `yaml:"service_account_id"`
	// IdentityProviderID is the OpenAI workload identity provider (openai).
	IdentityProviderID string `yaml:"identity_provider_id"`
	// FederationRuleID, OrganizationID and WorkspaceID select the Anthropic
	// federation rule, organization and workspace (anthropic).
	FederationRuleID string `yaml:"federation_rule_id"`
	OrganizationID   string `yaml:"organization_id"`
	WorkspaceID      string `yaml:"workspace_id"`
}

// Configured reports whether any workload identity field is set.
func (w WorkloadIdentityConfig) Configured() bool {
	return w != WorkloadIdentityConfig{}
}

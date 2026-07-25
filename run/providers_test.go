package run

import (
	"slices"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/providers"
)

// TestDefaultProviderFactoryCredentialForms pins the credential form the
// shipped provider registrations produce — the contract the admin API serves
// and the dashboard renders. A registration that stops declaring a field its
// adapter reads would otherwise fail silently: the field disappears from the
// form, and a stored value for it is no longer offered for editing.
func TestDefaultProviderFactoryCredentialForms(t *testing.T) {
	schemas := map[string]providers.CredentialSchema{}
	for _, schema := range defaultProviderFactory(&config.Config{}).CredentialSchemas() {
		schemas[schema.Type] = schema
	}

	tests := []struct {
		providerType string
		fields       []string // exact, in display order
		required     []string
		absent       []string
		options      map[string][]string
	}{
		{
			// The plain shape every API-key provider derives.
			providerType: "openai",
			fields:       []string{"api_keys", "base_url", "models"},
			required:     []string{"api_keys"},
			absent:       []string{"api_version", "vertex_project"},
		},
		{
			// A deployment URL is the provider, so it is required, and Azure
			// is the one type that takes an API version.
			providerType: "azure",
			fields:       []string{"api_keys", "base_url", "api_version", "models"},
			required:     []string{"api_keys", "base_url"},
		},
		{
			// Keyless: the endpoint is the whole configuration.
			providerType: "ollama",
			fields:       []string{"api_keys", "base_url", "models"},
			required:     nil,
		},
		{
			// Authenticates through the AWS SDK credential chain, never a key.
			providerType: "bedrock",
			fields:       []string{"base_url", "models"},
			absent:       []string{"api_keys"},
		},
		{
			// Bearer token or AWS_BEARER_TOKEN_BEDROCK, plus a request shape.
			providerType: "bedrock-mantle",
			fields:       []string{"api_keys", "base_url", "api_mode", "models"},
			required:     nil,
			options:      map[string][]string{"api_mode": {"auto", "openai", "standard"}},
		},
		{
			// One adapter, two backends: an AI Studio key, or Google
			// credentials once it points at Vertex.
			providerType: "gemini",
			fields: []string{
				"api_keys", "backend", "base_url", "api_mode", "auth_type",
				"vertex_project", "vertex_location", "service_account_json",
				"service_account_file", "service_account_json_base64", "gcp_scope", "models",
			},
			required: nil,
			options: map[string][]string{
				"backend":   {"aistudio", "vertex"},
				"api_mode":  {"native", "openai_compatible"},
				"auth_type": {"api_key", "gcp_adc", "gcp_service_account"},
			},
		},
		{
			// No API key at all; api_mode reaches the Gemini adapter Vertex
			// delegates translation to (VERTEX_API_MODE).
			providerType: "vertex",
			fields: []string{
				"auth_type", "vertex_project", "vertex_location", "service_account_json",
				"service_account_file", "service_account_json_base64", "base_url",
				"api_mode", "gcp_scope", "models",
			},
			absent:  []string{"api_keys"},
			options: map[string][]string{"api_mode": {"native", "openai_compatible"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.providerType, func(t *testing.T) {
			schema, ok := schemas[tt.providerType]
			if !ok {
				t.Fatalf("no credential schema for provider type %q", tt.providerType)
			}

			var names []string
			for _, field := range schema.Fields {
				names = append(names, field.Name)
			}
			if !slices.Equal(names, tt.fields) {
				t.Errorf("fields = %v, want %v", names, tt.fields)
			}
			for _, name := range tt.required {
				field, found := schema.Field(name)
				if !found || !field.Required {
					t.Errorf("%s.Required = %v (present=%v), want true", name, field.Required, found)
				}
			}
			for _, name := range tt.absent {
				if schema.Accepts(name) {
					t.Errorf("Accepts(%s) = true, want false for provider type %q", name, tt.providerType)
				}
			}
			for name, want := range tt.options {
				field, _ := schema.Field(name)
				if !slices.Equal(field.Options, want) {
					t.Errorf("%s.Options = %v, want %v", name, field.Options, want)
				}
			}
			// Every field a form renders must be one the API accepts, or the
			// value would be dropped on save.
			for _, name := range names {
				if !slices.Contains(credentialPayloadFields, name) {
					t.Errorf("field %q is not part of the upsert payload", name)
				}
			}
		})
	}
}

// credentialPayloadFields mirrors the upsert request's credential keys.
var credentialPayloadFields = []string{
	"api_keys", "base_url", "api_version", "backend", "auth_type", "api_mode",
	"vertex_project", "vertex_location", "service_account_file", "service_account_json",
	"service_account_json_base64", "gcp_scope", "models",
}

func TestDefaultProviderFactoryRegistersAllProviderTypes(t *testing.T) {
	expected := []string{
		"anthropic", "azure", "bailian", "bedrock", "bedrock-mantle", "cohere", "deepseek", "fireworks",
		"gemini", "groq", "kilo", "kimicode", "meta", "minimax", "ollama", "openai", "opencode_go",
		"openrouter", "oracle", "vertex", "vllm", "xai", "xiaomi", "zai",
	}

	for _, metricsEnabled := range []bool{false, true} {
		cfg := &config.Config{}
		cfg.Metrics.Enabled = metricsEnabled

		factory := defaultProviderFactory(cfg)
		got := factory.RegisteredTypes()
		slices.Sort(got)

		if !slices.Equal(got, expected) {
			t.Errorf("metrics=%v: registered types = %v, want %v", metricsEnabled, got, expected)
		}
	}
}

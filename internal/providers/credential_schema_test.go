package providers

import (
	"slices"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

// schemaTestFactory registers one provider type per DiscoveryConfig under test.
func schemaTestFactory(t *testing.T, specs map[string]DiscoveryConfig) *ProviderFactory {
	t.Helper()
	factory := NewProviderFactory()
	for providerType, spec := range specs {
		factory.Add(Registration{
			Type:      providerType,
			New:       func(ProviderConfig, ProviderOptions) core.Provider { return &registryMockProvider{} },
			Discovery: spec,
		})
	}
	return factory
}

func fieldNames(schema CredentialSchema) []string {
	names := make([]string, 0, len(schema.Fields))
	for _, field := range schema.Fields {
		names = append(names, field.Name)
	}
	return names
}

// The discovery flags a provider already declares for env/YAML resolution
// decide its form, so the two can never disagree about what a type needs.
func TestCredentialSchemas_DerivesTheFormFromDiscoveryFlags(t *testing.T) {
	tests := []struct {
		name     string
		spec     DiscoveryConfig
		fields   []string
		required []string
		advanced []string
	}{
		{
			name:     "an API key against one endpoint",
			spec:     DiscoveryConfig{DefaultBaseURL: "https://api.example.com/v1"},
			fields:   []string{"api_keys", "base_url", "models"},
			required: []string{"api_keys"},
			// Nothing else to configure once the key is filled in.
			advanced: []string{"base_url", "models"},
		},
		{
			name:     "keyless",
			spec:     DiscoveryConfig{DefaultBaseURL: "http://localhost:11434", AllowAPIKeyless: true},
			fields:   []string{"api_keys", "base_url", "models"},
			required: nil,
			// With no key to fill in, the endpoint is the configuration.
			advanced: []string{"models"},
		},
		{
			name:     "an endpoint the operator must name",
			spec:     DiscoveryConfig{RequireBaseURL: true, SupportsAPIVersion: true},
			fields:   []string{"api_keys", "base_url", "api_version", "models"},
			required: []string{"api_keys", "base_url"},
			advanced: []string{"models"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schema := credentialSchema("under-test", tt.spec)

			if got := fieldNames(schema); !equalStrings(got, tt.fields) {
				t.Fatalf("fields = %v, want %v", got, tt.fields)
			}
			if schema.DefaultBaseURL != tt.spec.DefaultBaseURL {
				t.Errorf("DefaultBaseURL = %q, want %q", schema.DefaultBaseURL, tt.spec.DefaultBaseURL)
			}
			for _, field := range schema.Fields {
				if want := slices.Contains(tt.required, field.Name); field.Required != want {
					t.Errorf("%s.Required = %v, want %v", field.Name, field.Required, want)
				}
				if want := slices.Contains(tt.advanced, field.Name); field.Advanced != want {
					t.Errorf("%s.Advanced = %v, want %v", field.Name, field.Advanced, want)
				}
			}
			// A plain provider type is offered none of Google's auth fields.
			if schema.Accepts(CredentialFieldVertexProject) {
				t.Error("Accepts(vertex_project) = true, want false")
			}
		})
	}
}

// CredentialSchemas covers every registered type, ordered by name so a type
// picker is stable.
func TestCredentialSchemas_CoversEveryTypeInOrder(t *testing.T) {
	factory := schemaTestFactory(t, map[string]DiscoveryConfig{
		"keyed":    {},
		"keyless":  {AllowAPIKeyless: true},
		"endpoint": {RequireBaseURL: true},
	})

	var types []string
	for _, schema := range factory.CredentialSchemas() {
		types = append(types, schema.Type)
	}
	if want := []string{"endpoint", "keyed", "keyless"}; !equalStrings(types, want) {
		t.Errorf("schema types = %v, want %v", types, want)
	}
}

func TestCredentialSchemas_UsesTheRegistrationsDeclaredForm(t *testing.T) {
	factory := schemaTestFactory(t, map[string]DiscoveryConfig{
		"google": {
			CredentialFields: []CredentialField{
				{Name: CredentialFieldAuthType, Options: []string{"gcp_adc", "gcp_service_account"}},
				{Name: CredentialFieldVertexProject},
				{Name: CredentialFieldBaseURL, Advanced: true},
			},
		},
	})

	schema := factory.CredentialSchemas()[0]
	if got, want := fieldNames(schema), []string{"auth_type", "vertex_project", "base_url", "models"}; !equalStrings(got, want) {
		t.Fatalf("fields = %v, want %v (declared order, models appended)", got, want)
	}
	// A type that authenticates another way must not offer an API key field.
	if schema.Accepts(CredentialFieldAPIKeys) {
		t.Error("Accepts(api_keys) = true for a declared keyless form, want false")
	}
	authType, _ := schema.Field(CredentialFieldAuthType)
	if len(authType.Options) != 2 {
		t.Errorf("auth_type.Options = %v, want the two declared values", authType.Options)
	}
}

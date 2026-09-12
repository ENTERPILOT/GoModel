package providers

import (
	"slices"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			fields:   []string{"api_keys", "base_url", "session_sticky_keys", "models"},
			required: []string{"api_keys"},
			// Nothing else to configure once the key is filled in.
			advanced: []string{"base_url", "session_sticky_keys", "models"},
		},
		{
			name:     "keyless",
			spec:     DiscoveryConfig{DefaultBaseURL: "http://localhost:11434", AllowAPIKeyless: true},
			fields:   []string{"api_keys", "base_url", "session_sticky_keys", "models"},
			required: nil,
			// With no key to fill in, the endpoint is the configuration.
			advanced: []string{"session_sticky_keys", "models"},
		},
		{
			name:     "an endpoint the operator must name",
			spec:     DiscoveryConfig{RequireBaseURL: true, SupportsAPIVersion: true},
			fields:   []string{"api_keys", "base_url", "api_version", "session_sticky_keys", "models"},
			required: []string{"api_keys", "base_url"},
			advanced: []string{"session_sticky_keys", "models"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schema := credentialSchema("under-test", tt.spec)
			got := fieldNames(schema)
			require.True(t, equalStrings(got, tt.fields), "fields = %v, want %v", got, tt.fields)
			assert.Equal(t, tt.spec.DefaultBaseURL, schema.DefaultBaseURL)

			for _, field := range schema.Fields {
				want := slices.Contains(tt.required, field.Name)
				assert.Equal(t, want, field.Required)
				want = slices.Contains(tt.advanced, field.Name)
				assert.Equal(t, want, field.Advanced)
			}
			// A plain provider type is offered none of Google's auth fields.
			assert.False(t, schema.Accepts(CredentialFieldVertexProject))
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
	want := []string{"endpoint", "keyed", "keyless"}
	assert.True(t, equalStrings(types, want), "schema types = %v, want %v", types, want)
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
	require.Equal(t, []string{"auth_type", "vertex_project", "base_url", "models"}, fieldNames(schema), "declared order, models appended")
	// A type that authenticates another way must not offer an API key field.
	assert.False(t, schema.Accepts(CredentialFieldAPIKeys))

	authType, _ := schema.Field(CredentialFieldAuthType)
	assert.Len(t, authType.Options, 2)
}

package providers

import (
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

func TestCredentialSchemas_DerivesTheFormFromDiscoveryFlags(t *testing.T) {
	factory := schemaTestFactory(t, map[string]DiscoveryConfig{
		"keyed":    {DefaultBaseURL: "https://api.example.com/v1"},
		"keyless":  {DefaultBaseURL: "http://localhost:11434", AllowAPIKeyless: true},
		"endpoint": {RequireBaseURL: true, SupportsAPIVersion: true},
	})

	schemas := factory.CredentialSchemas()
	if len(schemas) != 3 {
		t.Fatalf("CredentialSchemas() returned %d schemas, want 3", len(schemas))
	}
	// Sorted by type name, so a dashboard's type picker is stable.
	if schemas[0].Type != "endpoint" || schemas[1].Type != "keyed" || schemas[2].Type != "keyless" {
		t.Fatalf("schema order = %q/%q/%q, want endpoint/keyed/keyless", schemas[0].Type, schemas[1].Type, schemas[2].Type)
	}

	byType := map[string]CredentialSchema{}
	for _, schema := range schemas {
		byType[schema.Type] = schema
	}

	keyed := byType["keyed"]
	if got, want := fieldNames(keyed), []string{"api_keys", "base_url", "models"}; !equalStrings(got, want) {
		t.Errorf("keyed fields = %v, want %v", got, want)
	}
	if keyed.DefaultBaseURL != "https://api.example.com/v1" {
		t.Errorf("keyed DefaultBaseURL = %q, want the registration's default", keyed.DefaultBaseURL)
	}
	apiKeys, _ := keyed.Field(CredentialFieldAPIKeys)
	if !apiKeys.Required {
		t.Error("api_keys.Required = false for a keyed provider type, want true")
	}
	// Nothing else to configure once the key is filled in, so the base URL
	// folds away.
	baseURL, _ := keyed.Field(CredentialFieldBaseURL)
	if !baseURL.Advanced || baseURL.Required {
		t.Errorf("keyed base_url = %+v, want optional and advanced", baseURL)
	}

	keylessAPIKeys, _ := byType["keyless"].Field(CredentialFieldAPIKeys)
	if keylessAPIKeys.Required {
		t.Error("api_keys.Required = true for an AllowAPIKeyless type, want false")
	}
	// With no key to fill in, the endpoint is the whole configuration.
	keylessBaseURL, _ := byType["keyless"].Field(CredentialFieldBaseURL)
	if keylessBaseURL.Advanced {
		t.Error("keyless base_url.Advanced = true, want it shown up front")
	}

	endpoint := byType["endpoint"]
	if got, want := fieldNames(endpoint), []string{"api_keys", "base_url", "api_version", "models"}; !equalStrings(got, want) {
		t.Errorf("endpoint fields = %v, want %v", got, want)
	}
	endpointBaseURL, _ := endpoint.Field(CredentialFieldBaseURL)
	if !endpointBaseURL.Required {
		t.Error("base_url.Required = false for a RequireBaseURL type, want true")
	}
	if endpoint.Accepts("vertex_project") {
		t.Error("Accepts(vertex_project) = true for a plain endpoint type, want false")
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

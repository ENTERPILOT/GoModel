package providers

import (
	"errors"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

// assertCredentialField requires err to be a field-scoped rejection naming
// field, which is what lets the admin API answer with an error param the
// dashboard can pin to one input.
func assertCredentialField(t *testing.T, err error, field string) {
	t.Helper()
	var fieldErr *CredentialFieldError
	if !errors.As(err, &fieldErr) {
		t.Fatalf("error = %v (%T), want a *CredentialFieldError naming %q", err, err, field)
	}
	if fieldErr.Field != field {
		t.Fatalf("error field = %q, want %q (message: %s)", fieldErr.Field, field, fieldErr.Message)
	}
}

func TestValidateCredential_RequiredFields(t *testing.T) {
	keyed := credentialSchema("keyed", DiscoveryConfig{})
	endpoint := credentialSchema("endpoint", DiscoveryConfig{RequireBaseURL: true})
	keyless := credentialSchema("keyless", DiscoveryConfig{AllowAPIKeyless: true})

	tests := []struct {
		name   string
		cred   ManagedProviderCredential
		schema CredentialSchema
		field  string // "" when the credential is valid
	}{
		{
			name:   "missing key on a keyed type",
			cred:   ManagedProviderCredential{Name: "p", Type: "keyed"},
			schema: keyed,
			field:  CredentialFieldAPIKeys,
		},
		{
			name:   "blank key row",
			cred:   ManagedProviderCredential{Name: "p", Type: "keyed", APIKeys: []string{"sk-real", "  "}},
			schema: keyed,
			field:  CredentialFieldAPIKeys,
		},
		{
			name:   "missing base URL on a type that requires one",
			cred:   ManagedProviderCredential{Name: "p", Type: "endpoint", APIKeys: []string{"sk-real"}},
			schema: endpoint,
			field:  CredentialFieldBaseURL,
		},
		{
			name:   "keyless type needs nothing",
			cred:   ManagedProviderCredential{Name: "p", Type: "keyless"},
			schema: keyless,
		},
		{
			name:   "keyed type with a key",
			cred:   ManagedProviderCredential{Name: "p", Type: "keyed", APIKeys: []string{"sk-real"}},
			schema: keyed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCredential(tt.cred, tt.schema)
			if tt.field == "" {
				if err != nil {
					t.Fatalf("validateCredential() error = %v, want nil", err)
				}
				return
			}
			assertCredentialField(t, err, tt.field)
		})
	}
}

// An enumerated field's options are what a form should offer, not a
// whitelist: every provider that has one also accepts other spellings of the
// same value, and config.yaml/env callers already use them, so the admin API
// must not be the stricter surface.
func TestValidateCredential_AcceptsSpellingsOutsideAFieldsOptions(t *testing.T) {
	schema := credentialSchema("modal", DiscoveryConfig{
		AllowAPIKeyless: true,
		CredentialFields: []CredentialField{
			{Name: CredentialFieldAPIMode, Options: []string{"native", "openai_compatible"}},
		},
	})

	for _, mode := range []string{"", "native", "openai_compatible", "compat", "GENERATE_CONTENT"} {
		if err := validateCredential(ManagedProviderCredential{Name: "p", Type: "modal", APIMode: mode}, schema); err != nil {
			t.Errorf("validateCredential(api_mode=%q) error = %v, want nil", mode, err)
		}
	}
}

// Google-backed rows accept several valid field combinations rather than one
// required field per row, so they get their own cross-field rules — and each
// still points at the field to fix.
func TestValidateCredential_GoogleAuth(t *testing.T) {
	// The Vertex registration's own form: Google credentials, no API key.
	schema := credentialSchema("vertex", DiscoveryConfig{
		CredentialFields: []CredentialField{
			{Name: CredentialFieldAuthType, Options: []string{"gcp_adc", "gcp_service_account"}},
			{Name: CredentialFieldVertexProject},
			{Name: CredentialFieldVertexLocation},
			{Name: CredentialFieldServiceAccountJSON},
			{Name: CredentialFieldBaseURL, Advanced: true},
		},
	})

	tests := []struct {
		name  string
		cred  ManagedProviderCredential
		field string
	}{
		{
			name:  "no endpoint and no project",
			cred:  ManagedProviderCredential{Name: "v", Type: "vertex"},
			field: CredentialFieldVertexProject,
		},
		{
			name:  "project without location",
			cred:  ManagedProviderCredential{Name: "v", Type: "vertex", VertexProject: "p"},
			field: CredentialFieldVertexLocation,
		},
		{
			name:  "service account auth with no service account",
			cred:  ManagedProviderCredential{Name: "v", Type: "vertex", VertexProject: "p", VertexLocation: "us-central1", AuthType: "gcp_service_account"},
			field: CredentialFieldServiceAccountJSON,
		},
		{
			name:  "an API key does not authenticate against Vertex",
			cred:  ManagedProviderCredential{Name: "v", Type: "vertex", VertexProject: "p", VertexLocation: "us-central1", AuthType: "api_key"},
			field: CredentialFieldAuthType,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertCredentialField(t, validateCredential(tt.cred, schema), tt.field)
		})
	}

	valid := []ManagedProviderCredential{
		{Name: "v", Type: "vertex", VertexProject: "p", VertexLocation: "us-central1"},
		{Name: "v", Type: "vertex", BaseURL: "https://us-central1-aiplatform.googleapis.com"},
		{Name: "v", Type: "vertex", VertexProject: "p", VertexLocation: "us-central1", AuthType: "gcp_service_account", ServiceAccountFile: "/etc/sa.json"},
	}
	for _, cred := range valid {
		if err := validateCredential(cred, schema); err != nil {
			t.Errorf("validateCredential(%+v) error = %v, want nil", cred, err)
		}
	}
}

// A Gemini row only has to satisfy Google's rules once it points at Vertex;
// against AI Studio it is a plain API-key provider.
func TestValidateCredential_GeminiOnlyNeedsGoogleAuthOnVertex(t *testing.T) {
	schema := credentialSchema("gemini", DiscoveryConfig{
		CredentialFields: []CredentialField{
			{Name: CredentialFieldAPIKeys},
			{Name: CredentialFieldBackend, Options: []string{"aistudio", "vertex"}},
			{Name: CredentialFieldVertexProject},
		},
	})

	aiStudio := ManagedProviderCredential{Name: "g", Type: "gemini", APIKeys: []string{"AIza-real"}}
	if err := validateCredential(aiStudio, schema); err != nil {
		t.Fatalf("validateCredential(AI Studio) error = %v, want nil", err)
	}

	onVertex := aiStudio
	onVertex.Backend = "vertex"
	assertCredentialField(t, validateCredential(onVertex, schema), CredentialFieldVertexProject)
}

// A credential that cannot authenticate must not reach the store: an operator
// who fixes the mistake should not first have to clean up a broken row.
// Resolution knows field combinations the form schema cannot express — a
// Gemini row whose Vertex fields are set without a backend to match resolves
// like a plain API-key provider and needs a key the form never asked for.
// Those rejections must still read as bad input (a field-scoped 400), not as
// a gateway fault, and must leave nothing behind.
func TestCredentialsService_UpsertReportsAnUnresolvableRowAgainstAField(t *testing.T) {
	ctx := t.Context()
	factory := NewProviderFactory()
	factory.Add(Registration{
		Type: "conditional",
		New:  func(ProviderConfig, ProviderOptions) core.Provider { return &registryMockProvider{} },
		// The form does not require a key (another auth path may cover it),
		// but nothing else here resolves, so resolution still demands one.
		Discovery: DiscoveryConfig{CredentialFields: []CredentialField{{Name: CredentialFieldAPIKeys}}},
	})
	store := newFakeCredentialStore()

	svc, err := NewCredentialsService(ctx, factory, NewModelRegistry(), store, nil, config.ResilienceConfig{})
	if err != nil {
		t.Fatalf("NewCredentialsService() error = %v", err)
	}

	err = svc.Upsert(ctx, ManagedProviderCredential{Name: "half-configured", Type: "conditional", Enabled: true})
	assertCredentialField(t, err, CredentialFieldAPIKeys)
	if _, getErr := store.Get(ctx, "half-configured"); !errors.Is(getErr, ErrCredentialNotFound) {
		t.Errorf("store.Get() error = %v, want ErrCredentialNotFound (an unappliable credential must not be persisted)", getErr)
	}
}

func TestCredentialsService_UpsertRejectsAnIncompleteRowWithoutStoringIt(t *testing.T) {
	ctx := t.Context()
	factory := newCredentialsTestFactory(t)
	registry := NewModelRegistry()
	store := newFakeCredentialStore()

	svc, err := NewCredentialsService(ctx, factory, registry, store, nil, config.ResilienceConfig{})
	if err != nil {
		t.Fatalf("NewCredentialsService() error = %v", err)
	}

	err = svc.Upsert(ctx, ManagedProviderCredential{Name: "no-key", Type: "test", Enabled: true})
	assertCredentialField(t, err, CredentialFieldAPIKeys)

	if _, err := store.Get(ctx, "no-key"); !errors.Is(err, ErrCredentialNotFound) {
		t.Errorf("store.Get(no-key) error = %v, want ErrCredentialNotFound (a rejected credential must not be persisted)", err)
	}
	if registry.ProviderCount() != 0 {
		t.Errorf("ProviderCount() = %d, want 0", registry.ProviderCount())
	}
}

func TestCredentialsService_UpsertRejectsNameAndTypeByField(t *testing.T) {
	ctx := t.Context()
	factory := newCredentialsTestFactory(t)
	svc, err := NewCredentialsService(ctx, factory, NewModelRegistry(), newFakeCredentialStore(), nil, config.ResilienceConfig{})
	if err != nil {
		t.Fatalf("NewCredentialsService() error = %v", err)
	}

	assertCredentialField(t, svc.Upsert(ctx, ManagedProviderCredential{Type: "test"}), "name")
	assertCredentialField(t, svc.Upsert(ctx, ManagedProviderCredential{Name: "a/b", Type: "test"}), "name")
	assertCredentialField(t, svc.Upsert(ctx, ManagedProviderCredential{Name: "p"}), "type")
	assertCredentialField(t, svc.Upsert(ctx, ManagedProviderCredential{Name: "p", Type: "nope"}), "type")
}

// A disabled row still has to be a valid configuration: an operator enabling
// it later should not discover it never could have worked.
func TestCredentialsService_UpsertValidatesDisabledRows(t *testing.T) {
	ctx := t.Context()
	factory := newCredentialsTestFactory(t)
	svc, err := NewCredentialsService(ctx, factory, NewModelRegistry(), newFakeCredentialStore(), nil, config.ResilienceConfig{})
	if err != nil {
		t.Fatalf("NewCredentialsService() error = %v", err)
	}

	assertCredentialField(t,
		svc.Upsert(ctx, ManagedProviderCredential{Name: "off", Type: "test", Enabled: false}),
		CredentialFieldAPIKeys)
}

// The credential schema is what the admin form is built from, so every
// registered provider type must produce one.
func TestCredentialsService_CredentialSchemasCoverEveryRegisteredType(t *testing.T) {
	ctx := t.Context()
	factory := NewProviderFactory()
	factory.Add(Registration{Type: "a", New: func(ProviderConfig, ProviderOptions) core.Provider { return &registryMockProvider{} }})
	factory.Add(Registration{Type: "b", New: func(ProviderConfig, ProviderOptions) core.Provider { return &registryMockProvider{} }})

	svc, err := NewCredentialsService(ctx, factory, NewModelRegistry(), newFakeCredentialStore(), nil, config.ResilienceConfig{})
	if err != nil {
		t.Fatalf("NewCredentialsService() error = %v", err)
	}

	schemas := svc.CredentialSchemas()
	if len(schemas) != len(factory.RegisteredTypes()) {
		t.Fatalf("CredentialSchemas() = %d schemas, want one per registered type (%d)", len(schemas), len(factory.RegisteredTypes()))
	}
	if !svc.CredentialSchema("a").Accepts(CredentialFieldAPIKeys) {
		t.Error("CredentialSchema(a) does not accept api_keys, want the derived key form")
	}
	if !svc.CredentialSchema("unknown").Accepts(CredentialFieldModels) {
		t.Error("CredentialSchema(unknown) should still fall back to the derived form")
	}
}

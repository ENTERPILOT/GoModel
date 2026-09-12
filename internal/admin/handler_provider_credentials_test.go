package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/providers"
)

// providerCredentialsAdminFake is an in-memory ProviderCredentialsAdmin for
// handler tests: it stands in for *providers.CredentialsService without
// touching a real registry/factory.
type providerCredentialsAdminFake struct {
	rows       map[string]providers.ManagedProviderCredential
	managed    map[string]struct{}
	types      []string
	configured []providers.SanitizedProviderConfig
	upsertErr  error
	deleteErr  error
}

func (f *providerCredentialsAdminFake) ConfiguredProviders() []providers.SanitizedProviderConfig {
	return f.configured
}

func newProviderCredentialsAdminFake() *providerCredentialsAdminFake {
	return &providerCredentialsAdminFake{
		rows:    map[string]providers.ManagedProviderCredential{},
		managed: map[string]struct{}{},
		types:   []string{"openai", "anthropic", "ollama"},
	}
}

func (f *providerCredentialsAdminFake) addManaged(name string) {
	f.managed[name] = struct{}{}
}

func (f *providerCredentialsAdminFake) List(context.Context) ([]providers.ManagedProviderCredential, error) {
	names := make([]string, 0, len(f.rows))
	for name := range f.rows {
		names = append(names, name)
	}
	sort.Strings(names)
	rows := make([]providers.ManagedProviderCredential, 0, len(names))
	for _, name := range names {
		rows = append(rows, f.rows[name])
	}
	return rows, nil
}

func (f *providerCredentialsAdminFake) Get(_ context.Context, name string) (*providers.ManagedProviderCredential, error) {
	row, ok := f.rows[name]
	if !ok {
		return nil, providers.ErrCredentialNotFound
	}
	clone := row
	return &clone, nil
}

func (f *providerCredentialsAdminFake) Upsert(_ context.Context, cred providers.ManagedProviderCredential) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.rows[cred.Name] = cred
	return nil
}

func (f *providerCredentialsAdminFake) Delete(_ context.Context, name string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.rows[name]; !ok {
		return providers.ErrCredentialNotFound
	}
	delete(f.rows, name)
	return nil
}

func (f *providerCredentialsAdminFake) IsManaged(name string) bool {
	_, ok := f.managed[name]
	return ok
}

func (f *providerCredentialsAdminFake) RegisteredTypes() []string {
	return f.types
}

// CredentialSchemas returns a plain API-key form per known type; the
// per-provider shapes themselves are covered in the providers package.
func (f *providerCredentialsAdminFake) CredentialSchemas() []providers.CredentialSchema {
	schemas := make([]providers.CredentialSchema, 0, len(f.types))
	for _, providerType := range f.types {
		schemas = append(schemas, providers.CredentialSchema{
			Type: providerType,
			Fields: []providers.CredentialField{
				{Name: providers.CredentialFieldAPIKeys, Required: true},
				{Name: providers.CredentialFieldBaseURL, Advanced: true},
				{Name: providers.CredentialFieldModels, Advanced: true},
			},
		})
	}
	return schemas
}

func newProviderCredentialsHandler(fake *providerCredentialsAdminFake) *Handler {
	return NewHandler(nil, nil, WithProviderCredentials(fake))
}

func newProviderCredentialsHandlerWithConfigured(fake *providerCredentialsAdminFake, configured []providers.SanitizedProviderConfig) *Handler {
	return NewHandler(nil, nil, WithProviderCredentials(fake), WithConfiguredProviders(configured))
}

func TestListProviderCredentials_RedactsSecretsAndFlagsManaged(t *testing.T) {
	fake := newProviderCredentialsAdminFake()
	fake.rows["multi-key"] = providers.ManagedProviderCredential{
		Name:    "multi-key",
		Type:    "openai",
		APIKeys: []string{"sk-top-secret"},
		Enabled: true,
	}
	fake.rows["local-vertex"] = providers.ManagedProviderCredential{
		Name:               "local-vertex",
		Type:               "vertex",
		ServiceAccountJSON: `{"private_key":"hidden"}`,
		Enabled:            true,
	}
	h := newProviderCredentialsHandler(fake)

	c, rec := echotest.Get(t, "/admin/provider-credentials")
	err := h.ListProviderCredentials(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	for _, secret := range []string{"sk-top-secret", "hidden"} {
		assert.NotContains(t, rec.Body.String(), secret)
	}

	body := echotest.Decode[[]providerCredentialViewResponse](t, rec)

	byName := map[string]providerCredentialViewResponse{}
	for _, v := range body {
		byName[v.Name] = v
	}

	multiKey := byName["multi-key"]
	assert.False(t, multiKey.Managed)
	assert.Equal(t, []string{"***********"}, multiKey.APIKeys)

	vertex := byName["local-vertex"]
	assert.False(t, vertex.Managed)
	assert.Equal(t, "***********", vertex.ServiceAccountJSON)
}

func TestListProviderCredentials_IncludesDeclaredProvidersReadOnly(t *testing.T) {
	fake := newProviderCredentialsAdminFake()
	fake.addManaged("openai")
	fake.rows["my-vllm"] = providers.ManagedProviderCredential{
		Name:    "my-vllm",
		Type:    "vllm",
		Enabled: true,
	}
	h := newProviderCredentialsHandlerWithConfigured(fake, []providers.SanitizedProviderConfig{
		{Name: "openai", Type: "openai", BaseURL: "https://api.openai.com/v1"},
	})

	c, rec := echotest.Get(t, "/admin/provider-credentials")
	err := h.ListProviderCredentials(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body := echotest.Decode[[]providerCredentialViewResponse](t, rec)
	require.Len(t, body, 2)

	byName := map[string]providerCredentialViewResponse{}
	for _, v := range body {
		byName[v.Name] = v
	}

	declared, ok := byName["openai"]
	require.True(t, ok)
	assert.True(t, declared.Managed)
	assert.Equal(t, "https://api.openai.com/v1", declared.BaseURL)
	assert.Nil(t, declared.CreatedAt)
	assert.Nil(t, declared.UpdatedAt)

	stored, ok := byName["my-vllm"]
	require.True(t, ok)
	assert.False(t, stored.Managed)
}

func TestListProviderCredentials_ShadowedStoreRowIsHiddenNotDuplicated(t *testing.T) {
	fake := newProviderCredentialsAdminFake()
	fake.addManaged("openai")
	// A stale store row for a name that config/env has since claimed: the
	// service already treats this as inactive (CredentialsService.Reload
	// skips it), so the list must show only the declared row, not both.
	fake.rows["openai"] = providers.ManagedProviderCredential{Name: "openai", Type: "openai", APIKeys: []string{"sk-stale"}, Enabled: true}
	h := newProviderCredentialsHandlerWithConfigured(fake, []providers.SanitizedProviderConfig{
		{Name: "openai", Type: "openai"},
	})

	c, rec := echotest.Get(t, "/admin/provider-credentials")
	err := h.ListProviderCredentials(c)
	require.NoError(t, err)

	body := echotest.Decode[[]providerCredentialViewResponse](t, rec)
	require.Len(t, body, 1)
	assert.True(t, body[0].Managed)
	assert.NotContains(t, rec.Body.String(), "sk-stale")
}

func TestUpsertProviderCredential_CreatesAndRegistersImmediately(t *testing.T) {
	fake := newProviderCredentialsAdminFake()
	h := newProviderCredentialsHandler(fake)

	c, rec := echotest.Request(t, http.MethodPut, "/admin/provider-credentials", `{"name":"my-openai","type":"openai","api_keys":["sk-real"]}`)
	err := h.UpsertProviderCredential(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	stored, ok := fake.rows["my-openai"]
	require.True(t, ok)
	assert.Equal(t, []string{"sk-real"}, stored.APIKeys)
	assert.True(t, stored.Enabled)
	assert.NotContains(t, rec.Body.String(), "sk-real")
}

func TestUpsertProviderCredential_CanDisableSessionStickyKeys(t *testing.T) {
	fake := newProviderCredentialsAdminFake()
	h := newProviderCredentialsHandler(fake)

	c, rec := echotest.Request(t, http.MethodPut, "/admin/provider-credentials", `{"name":"my-openai","type":"openai","api_keys":["sk-real"],"session_sticky_keys":false}`)
	err := h.UpsertProviderCredential(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	stored := fake.rows["my-openai"]
	require.NotNil(t, stored.SessionStickyKeys)
	assert.False(t, *stored.SessionStickyKeys)
	response := echotest.Decode[providerCredentialViewResponse](t, rec)
	assert.False(t, response.SessionStickyKeys)
}

func TestUpsertProviderCredential_RedactedKeyPreservesStoredValuePositionally(t *testing.T) {
	fake := newProviderCredentialsAdminFake()
	fake.rows["multi"] = providers.ManagedProviderCredential{
		Name:    "multi",
		Type:    "openai",
		APIKeys: []string{"sk-one", "sk-two"},
		Enabled: true,
	}
	h := newProviderCredentialsHandler(fake)

	// Position 0 kept via "***", position 1 replaced with a new real value.
	c, rec := echotest.Request(t, http.MethodPut, "/admin/provider-credentials", `{"name":"multi","type":"openai","api_keys":["***","sk-new"]}`)
	err := h.UpsertProviderCredential(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.Equal(t, []string{"sk-one", "sk-new"}, fake.rows["multi"].APIKeys)
}

func TestUpsertProviderCredential_LongerRedactedKeyPreservesStoredValue(t *testing.T) {
	fake := newProviderCredentialsAdminFake()
	fake.rows["single"] = providers.ManagedProviderCredential{
		Name:    "single",
		Type:    "openai",
		APIKeys: []string{"sk-secret"},
		Enabled: true,
	}
	h := newProviderCredentialsHandler(fake)

	c, rec := echotest.Request(t, http.MethodPut, "/admin/provider-credentials", `{"name":"single","type":"openai","api_keys":["***********"]}`)
	err := h.UpsertProviderCredential(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.Equal(t, []string{"sk-secret"}, fake.rows["single"].APIKeys)
}

func TestUpsertProviderCredential_RedactedKeyBeyondStoredLengthIsRejected(t *testing.T) {
	fake := newProviderCredentialsAdminFake()
	fake.rows["single"] = providers.ManagedProviderCredential{
		Name:    "single",
		Type:    "openai",
		APIKeys: []string{"sk-one"},
		Enabled: true,
	}
	h := newProviderCredentialsHandler(fake)

	c, rec := echotest.Request(t, http.MethodPut, "/admin/provider-credentials", `{"name":"single","type":"openai","api_keys":["sk-one","***"]}`)
	err := h.UpsertProviderCredential(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestUpsertProviderCredential_RejectsManagedName(t *testing.T) {
	fake := newProviderCredentialsAdminFake()
	fake.addManaged("openai")
	h := newProviderCredentialsHandler(fake)

	c, rec := echotest.Request(t, http.MethodPut, "/admin/provider-credentials", `{"name":"openai","type":"openai","api_keys":["sk-real"]}`)
	err := h.UpsertProviderCredential(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.NotContains(t, fake.rows, "openai")
}

// errorParam reads the `param` a rejection blames, which is what lets the
// dashboard attach the message to one input instead of the whole form.
func errorParam(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Param   *string `json:"param"`
			Message string  `json:"message"`
		} `json:"error"`
	}
	err := json.Unmarshal(rec.Body.Bytes(), &body)
	require.NoError(t, err)
	require.NotNil(t, body.Error.Param, "error has no param (body=%s)", rec.Body.String())
	return *body.Error.Param
}

func TestUpsertProviderCredential_RejectionsNameTheOffendingField(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		param string
	}{
		{name: "missing name", body: `{"type":"openai","api_keys":["sk-real"]}`, param: "name"},
		{name: "name containing slash", body: `{"name":"my/provider","type":"openai","api_keys":["sk-real"]}`, param: "name"},
		{name: "missing type", body: `{"name":"x","api_keys":["sk-real"]}`, param: "type"},
		{name: "unknown type", body: `{"name":"x","type":"not-a-real-type","api_keys":["sk-real"]}`, param: "type"},
		{name: "redacted key with nothing to preserve", body: `{"name":"x","type":"openai","api_keys":["***********"]}`, param: "api_keys"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newProviderCredentialsAdminFake()
			h := newProviderCredentialsHandler(fake)
			c, rec := echotest.Request(t, http.MethodPut, "/admin/provider-credentials", tt.body)
			err := h.UpsertProviderCredential(c)
			require.NoError(t, err)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Equal(t, tt.param, errorParam(t, rec))
			assert.Empty(t, fake.rows)
		})
	}
}

// A credential the service rejects as unusable is a 400 the operator can act
// on, not a 502: it names the field to fix.
func TestUpsertProviderCredential_ServiceFieldErrorIsABadRequest(t *testing.T) {
	fake := newProviderCredentialsAdminFake()
	fake.upsertErr = &providers.CredentialFieldError{
		Field:   providers.CredentialFieldAPIKeys,
		Message: `at least one API key is required for provider type "openai"`,
	}
	h := newProviderCredentialsHandler(fake)

	c, rec := echotest.Request(t, http.MethodPut, "/admin/provider-credentials", `{"name":"x","type":"openai"}`)
	err := h.UpsertProviderCredential(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, providers.CredentialFieldAPIKeys, errorParam(t, rec))
}

func TestProviderCredentialTypes_ServesEachTypesCredentialForm(t *testing.T) {
	h := newProviderCredentialsHandler(newProviderCredentialsAdminFake())

	c, rec := echotest.Get(t, "/admin/provider-credentials/types")
	err := h.ProviderCredentialTypes(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body := echotest.Decode[[]providerCredentialTypeResponse](t, rec)
	require.Len(t, body, 3)
	assert.Equal(t, "openai", body[0].Type)
	require.NotEmpty(t, body[0].Fields)
	assert.Equal(t, providers.CredentialFieldAPIKeys, body[0].Fields[0].Name)
	assert.True(t, body[0].Fields[0].Required)
}

func TestDeleteProviderCredential(t *testing.T) {
	fake := newProviderCredentialsAdminFake()
	fake.rows["gone"] = providers.ManagedProviderCredential{Name: "gone", Type: "openai", APIKeys: []string{"sk"}, Enabled: true}
	h := newProviderCredentialsHandler(fake)

	c, rec := echotest.Request(t, http.MethodDelete, "/admin/provider-credentials/gone", nil, echotest.WithPathValue("name", "gone"))
	err := h.DeleteProviderCredential(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.NotContains(t, fake.rows, "gone")
}

func TestDeleteProviderCredential_RejectsManagedName(t *testing.T) {
	fake := newProviderCredentialsAdminFake()
	fake.addManaged("openai")
	fake.rows["openai"] = providers.ManagedProviderCredential{Name: "openai", Type: "openai", Enabled: true}
	h := newProviderCredentialsHandler(fake)

	c, rec := echotest.Request(t, http.MethodDelete, "/admin/provider-credentials/openai", nil, echotest.WithPathValue("name", "openai"))
	err := h.DeleteProviderCredential(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, fake.rows, "openai")
}

func TestDeleteProviderCredential_NotFound(t *testing.T) {
	fake := newProviderCredentialsAdminFake()
	h := newProviderCredentialsHandler(fake)

	c, rec := echotest.Request(t, http.MethodDelete, "/admin/provider-credentials/missing", nil, echotest.WithPathValue("name", "missing"))
	err := h.DeleteProviderCredential(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

func TestProviderCredentialsEndpointsReturn503WhenUnavailable(t *testing.T) {
	h := NewHandler(nil, nil)

	assertUnavailable := func(name string, err error, rec *httptest.ResponseRecorder) {
		t.Helper()
		require.NoError(t, err)
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, name)
	}

	listCtx, listRec := echotest.Get(t, "/admin/provider-credentials")
	assertUnavailable("ListProviderCredentials", h.ListProviderCredentials(listCtx), listRec)

	typesCtx, typesRec := echotest.Get(t, "/admin/provider-credentials/types")
	assertUnavailable("ProviderCredentialTypes", h.ProviderCredentialTypes(typesCtx), typesRec)

	putCtx, putRec := echotest.Request(t, http.MethodPut, "/admin/provider-credentials", `{"name":"x","type":"openai","api_keys":["sk"]}`)
	assertUnavailable("UpsertProviderCredential", h.UpsertProviderCredential(putCtx), putRec)

	deleteCtx, deleteRec := echotest.Request(t, http.MethodDelete, "/admin/provider-credentials/x", nil, echotest.WithPathValue("name", "x"))
	assertUnavailable("DeleteProviderCredential", h.DeleteProviderCredential(deleteCtx), deleteRec)
}

func TestUpsertProviderCredential_BubblesProviderErrorOnStoreFailure(t *testing.T) {
	fake := newProviderCredentialsAdminFake()
	fake.upsertErr = errors.New("disk full")
	h := newProviderCredentialsHandler(fake)

	c, rec := echotest.Request(t, http.MethodPut, "/admin/provider-credentials", `{"name":"x","type":"openai","api_keys":["sk"]}`)
	err := h.UpsertProviderCredential(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
}

// TestProviderStatus_ReportsCredentialServiceConfigForRuntimeProviders covers
// dashboard-registered providers: they are absent from the static configured
// snapshot, so their effective (resilience-merged) config must come from the
// credentials service instead of being reported as all zeros. A declared
// provider present in both sources keeps the declarative config.
func TestProviderStatus_ReportsCredentialServiceConfigForRuntimeProviders(t *testing.T) {
	registry := providers.NewModelRegistry()
	for _, name := range []string{"dash-openai", "openai_declared"} {
		registry.RegisterProviderWithNameAndType(&handlerMockProvider{
			models: &core.ModelsResponse{Object: "list", Data: []core.Model{{ID: "gpt-4o", Object: "model"}}},
		}, name, "openai")
	}
	err := registry.Initialize(context.Background())
	require.NoError(t, err)

	// A dashboard-registered provider installed after startup has no model
	// inventory until its first refresh; it must still report its effective
	// configuration and classify as configured rather than unknown.
	registry.RegisterProviderWithNameAndType(&handlerMockProvider{
		models: &core.ModelsResponse{Object: "list", Data: []core.Model{}},
	}, "dash-empty", "openai")

	resilience := func(maxRetries int) providers.SanitizedResilienceConfig {
		return providers.SanitizedResilienceConfig{
			Retry:          providers.SanitizedRetryConfig{MaxRetries: maxRetries, InitialBackoff: "1s", MaxBackoff: "30s", BackoffFactor: 2, JitterFactor: 0.1},
			CircuitBreaker: providers.SanitizedCircuitBreakerConfig{Enabled: true, FailureThreshold: 5, SuccessThreshold: 2, Timeout: "30s"},
		}
	}
	fake := newProviderCredentialsAdminFake()
	fake.configured = []providers.SanitizedProviderConfig{
		{Name: "dash-openai", Type: "openai", BaseURL: "https://dash.example.com/v1", Resilience: resilience(3)},
		{Name: "openai_declared", Type: "openai", Resilience: resilience(9)},
		{Name: "dash-empty", Type: "openai", BaseURL: "https://empty.example.com/v1", Resilience: resilience(3)},
	}
	declared := providers.SanitizedProviderConfig{Name: "openai_declared", Type: "openai", Resilience: resilience(1)}
	h := NewHandler(nil, registry, WithProviderCredentials(fake), WithConfiguredProviders([]providers.SanitizedProviderConfig{declared}))

	c, rec := echotest.Get(t, "/admin/providers/status")
	err = h.ProviderStatus(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	body := echotest.Decode[providerStatusResponse](t, rec)

	byName := make(map[string]providerStatusItemResponse, len(body.Providers))
	for _, provider := range body.Providers {
		byName[provider.Name] = provider
	}

	tests := []struct {
		name      string
		want      providers.SanitizedProviderConfig
		wantLabel string
	}{
		{name: "dash-openai", want: fake.configured[0], wantLabel: "Healthy"},
		{name: "openai_declared", want: declared, wantLabel: "Healthy"},
		{name: "dash-empty", want: fake.configured[2], wantLabel: "Configured"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item, ok := byName[tt.name]
			require.True(t, ok, "missing %s in %#v", tt.name, body.Providers)
			assert.Equal(t, tt.want.BaseURL, item.Config.BaseURL)
			assert.Equal(t, tt.want.Resilience, item.Config.Resilience)
			assert.Equal(t, tt.wantLabel, item.StatusLabel)
		})
	}
}

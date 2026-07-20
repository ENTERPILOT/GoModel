package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/providers"
)

// providerCredentialsAdminFake is an in-memory ProviderCredentialsAdmin for
// handler tests: it stands in for *providers.CredentialsService without
// touching a real registry/factory.
type providerCredentialsAdminFake struct {
	rows      map[string]providers.ManagedProviderCredential
	managed   map[string]struct{}
	types     []string
	upsertErr error
	deleteErr error
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

func newProviderCredentialsHandler(fake *providerCredentialsAdminFake) *Handler {
	return NewHandler(nil, nil, WithProviderCredentials(fake))
}

func newProviderCredentialsHandlerWithConfigured(fake *providerCredentialsAdminFake, configured []providers.SanitizedProviderConfig) *Handler {
	return NewHandler(nil, nil, WithProviderCredentials(fake), WithConfiguredProviders(configured))
}

func newProviderCredentialContext(method, target, body string) (*echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

func newProviderCredentialNameContext(method, target, name string) (*echo.Context, *httptest.ResponseRecorder) {
	c, rec := newProviderCredentialContext(method, target, "")
	c.SetPathValues(echo.PathValues{{Name: "name", Value: name}})
	return c, rec
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

	c, rec := newProviderCredentialContext(http.MethodGet, "/admin/provider-credentials", "")
	if err := h.ListProviderCredentials(c); err != nil {
		t.Fatalf("ListProviderCredentials() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	for _, secret := range []string{"sk-top-secret", "hidden"} {
		if containsString(rec.Body.String(), secret) {
			t.Fatalf("response leaked secret %q: %s", secret, rec.Body.String())
		}
	}

	var body []providerCredentialViewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	byName := map[string]providerCredentialViewResponse{}
	for _, v := range body {
		byName[v.Name] = v
	}

	multiKey := byName["multi-key"]
	if multiKey.Managed {
		t.Fatal("multi-key.Managed = true, want false (admin-store row)")
	}
	if len(multiKey.APIKeys) != 1 || multiKey.APIKeys[0] != redactedCredentialValue {
		t.Fatalf("multi-key.APIKeys = %#v, want one redacted entry", multiKey.APIKeys)
	}

	vertex := byName["local-vertex"]
	if vertex.Managed {
		t.Fatal("local-vertex.Managed = true, want false (admin-store row)")
	}
	if vertex.ServiceAccountJSON != redactedCredentialValue {
		t.Fatalf("local-vertex.ServiceAccountJSON = %q, want redacted", vertex.ServiceAccountJSON)
	}
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

	c, rec := newProviderCredentialContext(http.MethodGet, "/admin/provider-credentials", "")
	if err := h.ListProviderCredentials(c); err != nil {
		t.Fatalf("ListProviderCredentials() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}

	var body []providerCredentialViewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(body) != 2 {
		t.Fatalf("len(body) = %d, want 2 (declared openai + store-managed my-vllm): %#v", len(body), body)
	}
	byName := map[string]providerCredentialViewResponse{}
	for _, v := range body {
		byName[v.Name] = v
	}

	declared, ok := byName["openai"]
	if !ok {
		t.Fatal("declared provider 'openai' missing from list")
	}
	if !declared.Managed {
		t.Error("declared.Managed = false, want true (config/env-declared)")
	}
	if declared.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("declared.BaseURL = %q, want https://api.openai.com/v1", declared.BaseURL)
	}
	if declared.CreatedAt != nil || declared.UpdatedAt != nil {
		t.Errorf("declared timestamps = (%v, %v), want both nil/omitted", declared.CreatedAt, declared.UpdatedAt)
	}

	stored, ok := byName["my-vllm"]
	if !ok {
		t.Fatal("store-managed provider 'my-vllm' missing from list")
	}
	if stored.Managed {
		t.Error("stored.Managed = true, want false (admin-store row)")
	}
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

	c, rec := newProviderCredentialContext(http.MethodGet, "/admin/provider-credentials", "")
	if err := h.ListProviderCredentials(c); err != nil {
		t.Fatalf("ListProviderCredentials() error = %v", err)
	}
	var body []providerCredentialViewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(body) != 1 {
		t.Fatalf("len(body) = %d, want 1 (shadowed store row hidden): %#v", len(body), body)
	}
	if !body[0].Managed {
		t.Error("body[0].Managed = false, want true (the declared row must win)")
	}
	if containsString(rec.Body.String(), "sk-stale") {
		t.Fatalf("response leaked the shadowed store row's stale key: %s", rec.Body.String())
	}
}

func TestUpsertProviderCredential_CreatesAndRegistersImmediately(t *testing.T) {
	fake := newProviderCredentialsAdminFake()
	h := newProviderCredentialsHandler(fake)

	c, rec := newProviderCredentialContext(http.MethodPut, "/admin/provider-credentials", `{"name":"my-openai","type":"openai","api_keys":["sk-real"]}`)
	if err := h.UpsertProviderCredential(c); err != nil {
		t.Fatalf("UpsertProviderCredential() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	stored, ok := fake.rows["my-openai"]
	if !ok {
		t.Fatal("provider credential was not persisted")
	}
	if len(stored.APIKeys) != 1 || stored.APIKeys[0] != "sk-real" {
		t.Fatalf("stored.APIKeys = %#v, want [sk-real]", stored.APIKeys)
	}
	if !stored.Enabled {
		t.Fatal("stored.Enabled = false, want true (default)")
	}
	if containsString(rec.Body.String(), "sk-real") {
		t.Fatalf("response leaked the real API key: %s", rec.Body.String())
	}
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
	c, rec := newProviderCredentialContext(http.MethodPut, "/admin/provider-credentials", `{"name":"multi","type":"openai","api_keys":["***","sk-new"]}`)
	if err := h.UpsertProviderCredential(c); err != nil {
		t.Fatalf("UpsertProviderCredential() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	stored := fake.rows["multi"]
	if len(stored.APIKeys) != 2 || stored.APIKeys[0] != "sk-one" || stored.APIKeys[1] != "sk-new" {
		t.Fatalf("stored.APIKeys = %#v, want [sk-one sk-new]", stored.APIKeys)
	}
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

	c, rec := newProviderCredentialContext(http.MethodPut, "/admin/provider-credentials", `{"name":"single","type":"openai","api_keys":["sk-one","***"]}`)
	err := h.UpsertProviderCredential(c)
	if err != nil {
		t.Fatalf("UpsertProviderCredential() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestUpsertProviderCredential_RejectsManagedName(t *testing.T) {
	fake := newProviderCredentialsAdminFake()
	fake.addManaged("openai")
	h := newProviderCredentialsHandler(fake)

	c, rec := newProviderCredentialContext(http.MethodPut, "/admin/provider-credentials", `{"name":"openai","type":"openai","api_keys":["sk-real"]}`)
	if err := h.UpsertProviderCredential(c); err != nil {
		t.Fatalf("UpsertProviderCredential() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := fake.rows["openai"]; ok {
		t.Fatal("managed provider should not have been persisted")
	}
}

func TestUpsertProviderCredential_RejectsUnknownType(t *testing.T) {
	fake := newProviderCredentialsAdminFake()
	h := newProviderCredentialsHandler(fake)

	c, rec := newProviderCredentialContext(http.MethodPut, "/admin/provider-credentials", `{"name":"x","type":"not-a-real-type","api_keys":["sk-real"]}`)
	if err := h.UpsertProviderCredential(c); err != nil {
		t.Fatalf("UpsertProviderCredential() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestUpsertProviderCredential_RejectsNameContainingSlash(t *testing.T) {
	fake := newProviderCredentialsAdminFake()
	h := newProviderCredentialsHandler(fake)

	c, rec := newProviderCredentialContext(http.MethodPut, "/admin/provider-credentials", `{"name":"my/provider","type":"openai","api_keys":["sk-real"]}`)
	if err := h.UpsertProviderCredential(c); err != nil {
		t.Fatalf("UpsertProviderCredential() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := fake.rows["my/provider"]; ok {
		t.Fatal("a name containing '/' should not have been persisted")
	}
}

func TestDeleteProviderCredential(t *testing.T) {
	fake := newProviderCredentialsAdminFake()
	fake.rows["gone"] = providers.ManagedProviderCredential{Name: "gone", Type: "openai", APIKeys: []string{"sk"}, Enabled: true}
	h := newProviderCredentialsHandler(fake)

	c, rec := newProviderCredentialNameContext(http.MethodDelete, "/admin/provider-credentials/gone", "gone")
	if err := h.DeleteProviderCredential(c); err != nil {
		t.Fatalf("DeleteProviderCredential() error = %v", err)
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if _, ok := fake.rows["gone"]; ok {
		t.Fatal("provider credential was not deleted")
	}
}

func TestDeleteProviderCredential_RejectsManagedName(t *testing.T) {
	fake := newProviderCredentialsAdminFake()
	fake.addManaged("openai")
	fake.rows["openai"] = providers.ManagedProviderCredential{Name: "openai", Type: "openai", Enabled: true}
	h := newProviderCredentialsHandler(fake)

	c, rec := newProviderCredentialNameContext(http.MethodDelete, "/admin/provider-credentials/openai", "openai")
	if err := h.DeleteProviderCredential(c); err != nil {
		t.Fatalf("DeleteProviderCredential() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := fake.rows["openai"]; !ok {
		t.Fatal("managed provider should not have been deleted")
	}
}

func TestDeleteProviderCredential_NotFound(t *testing.T) {
	fake := newProviderCredentialsAdminFake()
	h := newProviderCredentialsHandler(fake)

	c, rec := newProviderCredentialNameContext(http.MethodDelete, "/admin/provider-credentials/missing", "missing")
	if err := h.DeleteProviderCredential(c); err != nil {
		t.Fatalf("DeleteProviderCredential() error = %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 body=%s", rec.Code, rec.Body.String())
	}
}

func TestProviderCredentialsEndpointsReturn503WhenUnavailable(t *testing.T) {
	h := NewHandler(nil, nil)

	assertUnavailable := func(name string, err error, rec *httptest.ResponseRecorder) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s error = %v", name, err)
		}
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, want 503", name, rec.Code)
		}
	}

	listCtx, listRec := newProviderCredentialContext(http.MethodGet, "/admin/provider-credentials", "")
	assertUnavailable("ListProviderCredentials", h.ListProviderCredentials(listCtx), listRec)

	typesCtx, typesRec := newProviderCredentialContext(http.MethodGet, "/admin/provider-credentials/types", "")
	assertUnavailable("ProviderCredentialTypes", h.ProviderCredentialTypes(typesCtx), typesRec)

	putCtx, putRec := newProviderCredentialContext(http.MethodPut, "/admin/provider-credentials", `{"name":"x","type":"openai","api_keys":["sk"]}`)
	assertUnavailable("UpsertProviderCredential", h.UpsertProviderCredential(putCtx), putRec)

	deleteCtx, deleteRec := newProviderCredentialNameContext(http.MethodDelete, "/admin/provider-credentials/x", "x")
	assertUnavailable("DeleteProviderCredential", h.DeleteProviderCredential(deleteCtx), deleteRec)
}

func TestUpsertProviderCredential_BubblesProviderErrorOnStoreFailure(t *testing.T) {
	fake := newProviderCredentialsAdminFake()
	fake.upsertErr = errors.New("disk full")
	h := newProviderCredentialsHandler(fake)

	c, rec := newProviderCredentialContext(http.MethodPut, "/admin/provider-credentials", `{"name":"x","type":"openai","api_keys":["sk"]}`)
	if err := h.UpsertProviderCredential(c); err != nil {
		t.Fatalf("UpsertProviderCredential() error = %v", err)
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 body=%s", rec.Code, rec.Body.String())
	}
}

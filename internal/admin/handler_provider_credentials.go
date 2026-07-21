package admin

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

// ProviderCredentialsAdmin is the narrow surface of *providers.CredentialsService
// the admin API needs, kept as an interface so handler tests can stub it.
type ProviderCredentialsAdmin interface {
	List(ctx context.Context) ([]providers.ManagedProviderCredential, error)
	Get(ctx context.Context, name string) (*providers.ManagedProviderCredential, error)
	Upsert(ctx context.Context, cred providers.ManagedProviderCredential) error
	Delete(ctx context.Context, name string) error
	IsManaged(name string) bool
	RegisteredTypes() []string
}

// redactedCredentialValue replaces secret values (API keys, service account
// JSON) in admin views. An all-asterisk value of at least three characters is
// accepted on upsert to keep the currently stored value, so older dashboard
// clients that send "***" remain compatible.
const redactedCredentialValue = "***********"

// upsertProviderCredentialRequest is the admin upsert contract for one
// provider credential. A value containing only three or more asterisks in
// api_keys or the service-account fields preserves the stored value at that
// position.
type upsertProviderCredentialRequest struct {
	Name                     string   `json:"name"`
	Type                     string   `json:"type"`
	APIKeys                  []string `json:"api_keys,omitempty"`
	BaseURL                  string   `json:"base_url,omitempty"`
	APIVersion               string   `json:"api_version,omitempty"`
	Backend                  string   `json:"backend,omitempty"`
	AuthType                 string   `json:"auth_type,omitempty"`
	APIMode                  string   `json:"api_mode,omitempty"`
	VertexProject            string   `json:"vertex_project,omitempty"`
	VertexLocation           string   `json:"vertex_location,omitempty"`
	ServiceAccountFile       string   `json:"service_account_file,omitempty"`
	ServiceAccountJSON       string   `json:"service_account_json,omitempty"`
	ServiceAccountJSONBase64 string   `json:"service_account_json_base64,omitempty"`
	GCPScope                 string   `json:"gcp_scope,omitempty"`
	Models                   []string `json:"models,omitempty"`
	Enabled                  *bool    `json:"enabled,omitempty"`
}

// providerCredentialViewResponse is the admin view of one provider
// credential: its definition (secrets redacted) plus whether it is read-only
// (config/env-declared).
type providerCredentialViewResponse struct {
	Name                     string     `json:"name"`
	Type                     string     `json:"type"`
	APIKeys                  []string   `json:"api_keys,omitempty"`
	BaseURL                  string     `json:"base_url,omitempty"`
	APIVersion               string     `json:"api_version,omitempty"`
	Backend                  string     `json:"backend,omitempty"`
	AuthType                 string     `json:"auth_type,omitempty"`
	APIMode                  string     `json:"api_mode,omitempty"`
	VertexProject            string     `json:"vertex_project,omitempty"`
	VertexLocation           string     `json:"vertex_location,omitempty"`
	ServiceAccountFile       string     `json:"service_account_file,omitempty"`
	ServiceAccountJSON       string     `json:"service_account_json,omitempty"`
	ServiceAccountJSONBase64 string     `json:"service_account_json_base64,omitempty"`
	GCPScope                 string     `json:"gcp_scope,omitempty"`
	Models                   []string   `json:"models,omitempty"`
	Enabled                  bool       `json:"enabled"`
	Managed                  bool       `json:"managed"`
	CreatedAt                *time.Time `json:"created_at,omitempty"`
	UpdatedAt                *time.Time `json:"updated_at,omitempty"`
}

// ListProviderCredentials handles GET /admin/provider-credentials.
//
// @Summary      List admin-managed model provider credentials
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   providerCredentialViewResponse
// @Failure      401  {object}  core.GatewayError
// @Failure      503  {object}  core.GatewayError
// @Router       /admin/provider-credentials [get]
func (h *Handler) ListProviderCredentials(c *echo.Context) error {
	if h.providerCredentials == nil {
		return handleError(c, featureUnavailableError("provider credentials feature is unavailable"))
	}
	rows, err := h.providerCredentials.List(c.Request().Context())
	if err != nil {
		return handleError(c, providerCredentialWriteError(err))
	}

	byName := make(map[string]providerCredentialViewResponse, len(rows)+len(h.configuredProviders))
	// Declarative (config.yaml/env) providers first: always read-only, and
	// take priority over any same-named store row that config/env now
	// shadows (CredentialsService.Reload skips those rows for registration,
	// so surfacing them here too would show a confusing duplicate/dead row).
	for _, cfg := range h.configuredProviders {
		view := h.declaredProviderCredentialView(cfg)
		byName[view.Name] = view
	}
	for _, row := range rows {
		if h.providerCredentials.IsManaged(row.Name) {
			continue
		}
		byName[row.Name] = h.providerCredentialView(row)
	}

	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]providerCredentialViewResponse, 0, len(names))
	for _, name := range names {
		result = append(result, byName[name])
	}
	return c.JSON(http.StatusOK, result)
}

// ProviderCredentialTypes handles GET /admin/provider-credentials/types.
//
// @Summary      List provider types the gateway can construct
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   string
// @Failure      401  {object}  core.GatewayError
// @Failure      503  {object}  core.GatewayError
// @Router       /admin/provider-credentials/types [get]
func (h *Handler) ProviderCredentialTypes(c *echo.Context) error {
	if h.providerCredentials == nil {
		return handleError(c, featureUnavailableError("provider credentials feature is unavailable"))
	}
	types := h.providerCredentials.RegisteredTypes()
	sort.Strings(types)
	return c.JSON(http.StatusOK, types)
}

// UpsertProviderCredential handles PUT /admin/provider-credentials.
//
// @Summary      Create or update one admin-managed provider credential
// @Description  Registers (or re-registers) the provider into the running gateway immediately, without a restart.
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        provider  body      upsertProviderCredentialRequest  true  "Provider credential definition"
// @Success      200       {object}  providerCredentialViewResponse
// @Failure      400       {object}  core.GatewayError
// @Failure      401       {object}  core.GatewayError
// @Failure      502       {object}  core.GatewayError
// @Failure      503       {object}  core.GatewayError
// @Router       /admin/provider-credentials [put]
func (h *Handler) UpsertProviderCredential(c *echo.Context) error {
	if h.providerCredentials == nil {
		return handleError(c, featureUnavailableError("provider credentials feature is unavailable"))
	}

	var req upsertProviderCredentialRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return handleError(c, core.NewInvalidRequestError("name is required", nil))
	}
	// "/" is the model-selector qualifier delimiter ("name/model"); a name
	// containing one would make the provider unreachable or ambiguous
	// through that syntax. CredentialsService.Upsert enforces this too, but
	// checking here gets a clean 400 instead of a 502-wrapped service error.
	if strings.Contains(name, "/") {
		return handleError(c, core.NewInvalidRequestError("name must not contain '/'", nil))
	}
	providerType := strings.TrimSpace(req.Type)
	if providerType == "" {
		return handleError(c, core.NewInvalidRequestError("type is required", nil))
	}
	if !slices.Contains(h.providerCredentials.RegisteredTypes(), providerType) {
		return handleError(c, core.NewInvalidRequestError("unknown provider type: "+providerType, nil))
	}
	if h.providerCredentials.IsManaged(name) {
		return handleError(c, core.NewInvalidRequestError("provider "+name+" is managed by config/env and is read-only", nil))
	}

	// Serialize provider-credential mutations: buildProviderCredentialUpsert
	// reads the current stored row (to resolve redacted placeholders) and then
	// writes it back, so two concurrent edits of the same name could
	// otherwise interleave and leave the store and registry inconsistent.
	h.mutationMu.Lock()
	defer h.mutationMu.Unlock()

	cred, err := h.buildProviderCredentialUpsert(c.Request().Context(), name, req)
	if err != nil {
		return handleError(c, err)
	}
	if err := h.providerCredentials.Upsert(c.Request().Context(), cred); err != nil {
		return handleError(c, providerCredentialWriteError(err))
	}

	stored, err := h.providerCredentials.Get(c.Request().Context(), name)
	if err != nil {
		return c.NoContent(http.StatusNoContent)
	}
	return c.JSON(http.StatusOK, h.providerCredentialView(*stored))
}

// DeleteProviderCredential handles DELETE /admin/provider-credentials/:name.
//
// @Summary      Delete one admin-managed provider credential
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        name  path  string  true  "Provider name"
// @Success      204   "No Content"
// @Failure      400   {object}  core.GatewayError
// @Failure      401   {object}  core.GatewayError
// @Failure      404   {object}  core.GatewayError
// @Failure      502   {object}  core.GatewayError
// @Failure      503   {object}  core.GatewayError
// @Router       /admin/provider-credentials/{name} [delete]
func (h *Handler) DeleteProviderCredential(c *echo.Context) error {
	if h.providerCredentials == nil {
		return handleError(c, featureUnavailableError("provider credentials feature is unavailable"))
	}
	h.mutationMu.Lock()
	defer h.mutationMu.Unlock()
	return deleteManagedResource(c, "provider", h.providerCredentials.IsManaged, h.providerCredentials.Delete, providers.ErrCredentialNotFound, providerCredentialWriteError)
}

// buildProviderCredentialUpsert maps the request onto a validated
// ManagedProviderCredential, resolving redacted placeholders against the
// stored row so the dashboard never has to re-submit real secrets on every
// edit.
func (h *Handler) buildProviderCredentialUpsert(ctx context.Context, name string, req upsertProviderCredentialRequest) (providers.ManagedProviderCredential, error) {
	current, err := h.providerCredentials.Get(ctx, name)
	if err != nil && !errors.Is(err, providers.ErrCredentialNotFound) {
		return providers.ManagedProviderCredential{}, providerCredentialWriteError(err)
	}

	apiKeys, err := mergeRedactedList(req.APIKeys, currentAPIKeys(current))
	if err != nil {
		return providers.ManagedProviderCredential{}, err
	}
	serviceAccountJSON, err := mergeRedactedValue(req.ServiceAccountJSON, currentField(current, func(c providers.ManagedProviderCredential) string { return c.ServiceAccountJSON }))
	if err != nil {
		return providers.ManagedProviderCredential{}, err
	}
	serviceAccountJSONBase64, err := mergeRedactedValue(req.ServiceAccountJSONBase64, currentField(current, func(c providers.ManagedProviderCredential) string { return c.ServiceAccountJSONBase64 }))
	if err != nil {
		return providers.ManagedProviderCredential{}, err
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	} else if current != nil {
		enabled = current.Enabled
	}

	cred := providers.ManagedProviderCredential{
		Name:                     name,
		Type:                     strings.TrimSpace(req.Type),
		APIKeys:                  apiKeys,
		BaseURL:                  strings.TrimSpace(req.BaseURL),
		APIVersion:               strings.TrimSpace(req.APIVersion),
		Backend:                  strings.TrimSpace(req.Backend),
		AuthType:                 strings.TrimSpace(req.AuthType),
		APIMode:                  strings.TrimSpace(req.APIMode),
		VertexProject:            strings.TrimSpace(req.VertexProject),
		VertexLocation:           strings.TrimSpace(req.VertexLocation),
		ServiceAccountFile:       strings.TrimSpace(req.ServiceAccountFile),
		ServiceAccountJSON:       serviceAccountJSON,
		ServiceAccountJSONBase64: serviceAccountJSONBase64,
		GCPScope:                 strings.TrimSpace(req.GCPScope),
		Models:                   req.Models,
		Enabled:                  enabled,
	}
	if current != nil {
		cred.CreatedAt = current.CreatedAt
	}
	return cred, nil
}

func currentAPIKeys(current *providers.ManagedProviderCredential) []string {
	if current == nil {
		return nil
	}
	return current.APIKeys
}

func currentField(current *providers.ManagedProviderCredential, get func(providers.ManagedProviderCredential) string) string {
	if current == nil {
		return ""
	}
	return get(*current)
}

// mergeRedactedList resolves all-asterisk placeholders in an ordered secret list
// (API key rotation) positionally against the stored list. A placeholder past
// the end of the stored list has nothing to preserve and is rejected, the
// same rule mergeRedactedMCPHeaders applies to headers.
func mergeRedactedList(incoming, stored []string) ([]string, error) {
	if incoming == nil {
		return nil, nil
	}
	merged := make([]string, len(incoming))
	for i, value := range incoming {
		if !isRedactedCredentialValue(value) {
			merged[i] = value
			continue
		}
		if i >= len(stored) {
			return nil, core.NewInvalidRequestError("api_keys entry is redacted but has no stored value to preserve; provide the real value", nil)
		}
		merged[i] = stored[i]
	}
	return merged, nil
}

// mergeRedactedValue resolves a single all-asterisk placeholder against the
// stored value.
func mergeRedactedValue(incoming, stored string) (string, error) {
	if !isRedactedCredentialValue(incoming) {
		return incoming, nil
	}
	if stored == "" {
		return "", core.NewInvalidRequestError("value is redacted but has no stored value to preserve; provide the real value", nil)
	}
	return stored, nil
}

// isRedactedCredentialValue recognizes both the current display mask and
// legacy masks emitted by older dashboard versions. Requiring at least three
// asterisks avoids treating a stray single character as an intentional
// preserve-secret sentinel.
func isRedactedCredentialValue(value string) bool {
	return len(value) >= 3 && strings.Trim(value, "*") == ""
}

// providerCredentialView converts one stored row into the admin response
// shape, redacting secrets.
func (h *Handler) providerCredentialView(cred providers.ManagedProviderCredential) providerCredentialViewResponse {
	resp := providerCredentialViewResponse{
		Name:               cred.Name,
		Type:               cred.Type,
		APIKeys:            redactList(cred.APIKeys),
		BaseURL:            cred.BaseURL,
		APIVersion:         cred.APIVersion,
		Backend:            cred.Backend,
		AuthType:           cred.AuthType,
		APIMode:            cred.APIMode,
		VertexProject:      cred.VertexProject,
		VertexLocation:     cred.VertexLocation,
		ServiceAccountFile: cred.ServiceAccountFile,
		GCPScope:           cred.GCPScope,
		Models:             cred.Models,
		Enabled:            cred.Enabled,
		Managed:            h.providerCredentials.IsManaged(cred.Name),
		CreatedAt:          nonZeroTime(cred.CreatedAt),
		UpdatedAt:          nonZeroTime(cred.UpdatedAt),
	}
	if cred.ServiceAccountJSON != "" {
		resp.ServiceAccountJSON = redactedCredentialValue
	}
	if cred.ServiceAccountJSONBase64 != "" {
		resp.ServiceAccountJSONBase64 = redactedCredentialValue
	}
	return resp
}

// declaredProviderCredentialView builds a read-only view row for one
// declarative (config.yaml/env) provider, so operators see the full picture
// of what is routable without config/env providers ever passing through the
// admin API's secret storage. SanitizedProviderConfig never carries API keys
// or service-account secrets, so those fields stay empty here by design —
// there is nothing to redact because nothing sensitive was ever loaded.
func (h *Handler) declaredProviderCredentialView(cfg providers.SanitizedProviderConfig) providerCredentialViewResponse {
	return providerCredentialViewResponse{
		Name:       cfg.Name,
		Type:       cfg.Type,
		BaseURL:    cfg.BaseURL,
		APIVersion: cfg.APIVersion,
		Models:     cfg.Models,
		Enabled:    true,
		Managed:    true,
	}
}

// nonZeroTime returns nil for a zero time.Time so the JSON response omits
// created_at/updated_at instead of encoding "0001-01-01T00:00:00Z".
func nonZeroTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// redactList keeps the entry count (so operators can see how many keys are
// configured) but replaces every value.
func redactList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	redacted := make([]string, len(values))
	for i := range values {
		redacted[i] = redactedCredentialValue
	}
	return redacted
}

// providerCredentialWriteError surfaces store/registry failures as 502,
// mirroring mcpServerWriteError.
func providerCredentialWriteError(err error) error {
	if err == nil {
		return nil
	}
	return core.NewProviderError("provider_credentials", http.StatusBadGateway, err.Error(), err)
}

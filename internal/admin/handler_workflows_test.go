package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/workflows"
)

// WithGuardrailsRegistry enables listing valid guardrail references for
// workflow authoring. Test-only seam: production wires the full guardrail
// service via WithGuardrailService.
func WithGuardrailsRegistry(registry guardrails.Catalog) Option {
	return func(h *Handler) {
		h.guardrails = registry
	}
}

type workflowTestStore struct {
	versions []workflows.Version
}

type workflowErrorEnvelope struct {
	Error struct {
		Type    string  `json:"type"`
		Message string  `json:"message"`
		Param   *string `json:"param"`
		Code    *string `json:"code"`
	} `json:"error"`
}

func (s *workflowTestStore) ListActive(context.Context) ([]workflows.Version, error) {
	result := make([]workflows.Version, 0, len(s.versions))
	for _, version := range s.versions {
		if version.Active {
			result = append(result, version)
		}
	}
	return result, nil
}

func (s *workflowTestStore) Get(_ context.Context, id string) (*workflows.Version, error) {
	for _, version := range s.versions {
		if version.ID == id {
			copy := version
			return &copy, nil
		}
	}
	return nil, workflows.ErrNotFound
}

func (s *workflowTestStore) Create(_ context.Context, input workflows.CreateInput) (*workflows.Version, error) {
	var scopeKey string
	switch {
	case input.Scope.Provider == "":
		if input.Scope.UserPath == "" {
			scopeKey = "global"
		} else {
			scopeKey = "path:" + input.Scope.UserPath
		}
	case input.Scope.Model == "":
		if input.Scope.UserPath == "" {
			scopeKey = "provider:" + input.Scope.Provider
		} else {
			scopeKey = "provider_path:" + input.Scope.Provider + ":" + input.Scope.UserPath
		}
	default:
		if input.Scope.UserPath == "" {
			scopeKey = "provider_model:" + input.Scope.Provider + ":" + input.Scope.Model
		} else {
			scopeKey = "provider_model_path:" + input.Scope.Provider + ":" + input.Scope.Model + ":" + input.Scope.UserPath
		}
	}
	workflowHash, err := workflowTestWorkflowHash(input.Payload)
	if err != nil {
		return nil, err
	}

	version := workflows.Version{
		ID:           "workflow-created",
		Scope:        input.Scope,
		ScopeKey:     scopeKey,
		Version:      len(s.versions) + 1,
		Active:       input.Activate,
		Name:         input.Name,
		Description:  input.Description,
		Payload:      input.Payload,
		WorkflowHash: workflowHash,
	}

	if input.Activate {
		for i := range s.versions {
			if s.versions[i].ScopeKey == scopeKey {
				s.versions[i].Active = false
			}
		}
	}

	s.versions = append(s.versions, version)
	return &version, nil
}

func (s *workflowTestStore) EnsureManagedDefaultGlobal(ctx context.Context, input workflows.CreateInput, workflowHash string) (*workflows.Version, error) {
	for _, version := range s.versions {
		if !version.Active || version.ScopeKey != "global" {
			continue
		}
		if !version.Managed {
			return nil, nil
		}
		if version.Name == input.Name && version.Description == input.Description && version.WorkflowHash == workflowHash {
			return nil, nil
		}
		break
	}
	return s.Create(ctx, input)
}

func (s *workflowTestStore) Deactivate(_ context.Context, id string) error {
	for i := range s.versions {
		if s.versions[i].ID == id && s.versions[i].Active {
			s.versions[i].Active = false
			return nil
		}
	}
	return workflows.ErrNotFound
}

func (s *workflowTestStore) Close() error { return nil }

func workflowTestWorkflowHash(payload workflows.Payload) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// globalWorkflow is the active global version every workflow store starts
// with; the scoped tests build on top of it.
func globalWorkflow(features workflows.FeatureFlags) workflows.Version {
	return workflows.Version{
		ID:           "global-workflow",
		ScopeKey:     "global",
		Version:      1,
		Active:       true,
		Name:         "global",
		Payload:      workflows.Payload{SchemaVersion: 1, Features: features},
		WorkflowHash: "hash-global",
	}
}

func newWorkflowRegistry(t *testing.T) *guardrails.Service {
	t.Helper()
	return newGuardrailService(t, guardrails.Definition{
		Name:   "policy-system",
		Type:   "system_prompt",
		Config: rawGuardrailConfig(t, map[string]any{"mode": "inject", "content": "be precise"}),
	})
}

func newWorkflowModelRegistry(t *testing.T) *providers.ModelRegistry {
	t.Helper()

	registry := providers.NewModelRegistry()
	registry.RegisterProviderWithType(&handlerMockProvider{
		models: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-5", Object: "model", OwnedBy: "openai"},
			},
		},
	}, "openai")
	err := registry.Initialize(context.Background())
	require.NoError(t, err)

	return registry
}

func newWorkflowHandler(t *testing.T, store workflows.Store, registry *guardrails.Service) *Handler {
	return newWorkflowHandlerWithModelRegistry(t, store, newWorkflowModelRegistry(t), registry)
}

func newWorkflowHandlerWithModelRegistry(t *testing.T, store workflows.Store, modelRegistry *providers.ModelRegistry, guardrailRegistry *guardrails.Service) *Handler {
	t.Helper()

	service, err := workflows.NewService(store, workflows.NewCompilerWithFeatureCaps(guardrailRegistry, core.DefaultWorkflowFeatures()))
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	return NewHandler(nil, modelRegistry, WithWorkflows(service), WithGuardrailsRegistry(guardrailRegistry))
}

func TestListWorkflows(t *testing.T) {
	failoverDisabled := false
	store := &workflowTestStore{versions: []workflows.Version{globalWorkflow(workflows.FeatureFlags{Cache: true, Audit: true, Usage: true, Failover: &failoverDisabled})}}

	h := newWorkflowHandler(t, store, nil)
	c, rec := echotest.Get(t, "/admin/workflows")
	err := h.ListWorkflows(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	body := echotest.Decode[[]workflows.View](t, rec)
	require.Len(t, body, 1)
	assert.Equal(t, "global", body[0].ScopeType)
	assert.Equal(t, "global", body[0].ScopeDisplay)
	require.NotNil(t, body[0].Payload.Features.Failover, "payload failover must stay explicit")
	assert.False(t, *body[0].Payload.Features.Failover)
	assert.True(t, body[0].EffectiveFeatures.Cache)
	assert.True(t, body[0].EffectiveFeatures.Audit)
	assert.True(t, body[0].EffectiveFeatures.Usage)
	assert.False(t, body[0].EffectiveFeatures.Failover)
}

func TestWorkflowsEndpointsReturn503WhenServiceUnavailable(t *testing.T) {
	h := NewHandler(nil, nil)
	withID := echotest.WithPathValue("id", "test-workflow")

	tests := []struct {
		name string
		run  func() (*echo.Context, *httptest.ResponseRecorder)
		call func(*echo.Context) error
	}{
		{"ListWorkflows", func() (*echo.Context, *httptest.ResponseRecorder) { return echotest.Get(t, "/admin/workflows") }, h.ListWorkflows},
		{"CreateWorkflow", func() (*echo.Context, *httptest.ResponseRecorder) { return echotest.Post(t, "/admin/workflows", `{}`) }, h.CreateWorkflow},
		{"DeactivateWorkflow", func() (*echo.Context, *httptest.ResponseRecorder) {
			return echotest.Post(t, "/admin/workflows/test-workflow/deactivate", nil, withID)
		}, h.DeactivateWorkflow},
		{"GetWorkflow", func() (*echo.Context, *httptest.ResponseRecorder) {
			return echotest.Get(t, "/admin/workflows/test-workflow", withID)
		}, h.GetWorkflow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, rec := tt.run()
			err := tt.call(c)
			require.NoError(t, err)
			require.Equal(t, http.StatusServiceUnavailable, rec.Code)

			envelope := echotest.Decode[workflowErrorEnvelope](t, rec)
			assert.Equal(t, "invalid_request_error", envelope.Error.Type)
			assert.Equal(t, "workflows feature is unavailable", envelope.Error.Message)
			assert.Nil(t, envelope.Error.Param)
			require.NotNil(t, envelope.Error.Code)
			assert.Equal(t, "feature_unavailable", *envelope.Error.Code)
		})
	}
}

func TestGetWorkflow(t *testing.T) {
	failoverEnabled := true
	store := &workflowTestStore{
		versions: []workflows.Version{
			globalWorkflow(workflows.FeatureFlags{Cache: true, Audit: true, Usage: true}),
			{
				ID:          "provider-workflow-v1",
				Scope:       workflows.Scope{Provider: "openai", Model: "gpt-5"},
				ScopeKey:    "provider_model:openai:gpt-5",
				Version:     1,
				Active:      false,
				Name:        "historical provider workflow",
				Description: "inactive but still queryable",
				Payload: workflows.Payload{
					SchemaVersion: 1,
					Features: workflows.FeatureFlags{
						Cache:      true,
						Audit:      true,
						Usage:      true,
						Guardrails: true,
						Failover:   &failoverEnabled,
					},
					Guardrails: []workflows.GuardrailStep{
						{Ref: "policy-system", Step: 10},
					},
				},
				WorkflowHash: "hash-provider-v1",
			},
		},
	}

	registry := newWorkflowRegistry(t)
	h := newWorkflowHandler(t, store, registry)
	c, rec := echotest.Get(t, "/admin/workflows/provider-workflow-v1", echotest.WithPathValue("id", "provider-workflow-v1"))
	err := h.GetWorkflow(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	rawBody := echotest.Decode[map[string]json.RawMessage](t, rec)

	var effectiveFeatures map[string]bool
	err = json.Unmarshal(rawBody["effective_features"], &effectiveFeatures)
	require.NoError(t, err)

	for _, key := range []string{"cache", "audit", "usage", "guardrails", "failover"} {
		assert.Contains(t, effectiveFeatures, key, "effective_features must use lower-case keys")
	}
	assert.True(t, effectiveFeatures["failover"], "renamed field must round-trip")
	assert.NotContains(t, effectiveFeatures, "Cache", "effective_features leaked a Go field key")

	body := echotest.Decode[workflows.View](t, rec)
	assert.Equal(t, "provider-workflow-v1", body.ID)
	assert.False(t, body.Active)
	assert.Equal(t, "provider_model", body.ScopeType)
	assert.Equal(t, "openai/gpt-5", body.ScopeDisplay)
	assert.True(t, body.Payload.Features.Usage)
	assert.True(t, body.Payload.Features.Audit)
	assert.True(t, body.Payload.Features.Guardrails)
	require.NotNil(t, body.Payload.Features.Failover)
	assert.True(t, *body.Payload.Features.Failover)
}

func TestCreateWorkflow_NormalizesScopeUserPath(t *testing.T) {
	store := &workflowTestStore{versions: []workflows.Version{globalWorkflow(workflows.FeatureFlags{Cache: true, Audit: true, Usage: true})}}
	h := newWorkflowHandler(t, store, nil)

	c, rec := echotest.Request(t, http.MethodPost, "/admin/workflows", `{
		"scope_provider_name":"openai",
		"scope_model":"gpt-5",
		"scope_user_path":" team//alpha/user/ ",
		"name":"Scoped workflow",
		"workflow_payload":{
			"schema_version":1,
			"features":{"cache":true,"audit":true,"usage":true,"guardrails":false}
		}
	}`)
	err := h.CreateWorkflow(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, rec.Code)

	version := echotest.Decode[workflows.Version](t, rec)
	assert.Equal(t, "/team/alpha/user", version.Scope.UserPath)
}

func TestListWorkflowGuardrails(t *testing.T) {
	registry := newWorkflowRegistry(t)
	h := NewHandler(nil, nil, WithGuardrailsRegistry(registry))
	c, rec := echotest.Get(t, "/admin/workflows/guardrails")
	err := h.ListWorkflowGuardrails(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	body := echotest.Decode[[]workflowGuardrailItem](t, rec)
	require.Len(t, body, 1)
	assert.Equal(t, "policy-system", body[0].Name)
	assert.Equal(t, []string{"prompt"}, body[0].Phases)
	assert.False(t, body[0].Mutates)

	// The full service reports type, phases, summary and the mutates flag
	// per instance (system_prompt edits the prompt).
	h = NewHandler(nil, nil, WithGuardrailService(registry))
	c, rec = echotest.Get(t, "/admin/workflows/guardrails")
	err = h.ListWorkflowGuardrails(c)
	require.NoError(t, err)

	body = echotest.Decode[[]workflowGuardrailItem](t, rec)
	require.Len(t, body, 1)
	assert.Equal(t, "system_prompt", body[0].Type)
	assert.NotEmpty(t, body[0].Summary)
	assert.Equal(t, []string{"prompt"}, body[0].Phases)
	assert.True(t, body[0].Mutates)
}

func TestCreateWorkflow(t *testing.T) {
	store := &workflowTestStore{versions: []workflows.Version{globalWorkflow(workflows.FeatureFlags{Cache: true, Audit: true, Usage: true})}}

	h := newWorkflowHandler(t, store, nil)

	c, rec := echotest.Request(t, http.MethodPost, "/admin/workflows", `{
		"scope_provider":"openai",
		"scope_model":"gpt-5",
		"name":"openai gpt-5",
		"description":"provider-model workflow",
		"workflow_payload":{
			"schema_version":1,
			"features":{"cache":false,"audit":true,"usage":true,"guardrails":false,"failover":false},
			"guardrails":[]
		}
	}`)
	err := h.CreateWorkflow(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, rec.Code)

	body := echotest.Decode[workflows.Version](t, rec)
	assert.Equal(t, "openai", body.Scope.Provider)
	assert.Equal(t, "gpt-5", body.Scope.Model)
	assert.Equal(t, "openai gpt-5", body.Name)
	require.NotNil(t, body.Payload.Features.Failover, "payload failover must stay explicit")
	assert.False(t, *body.Payload.Features.Failover)

	views, err := h.workflows.ListViews(context.Background())
	require.NoError(t, err)
	assert.Len(t, views, 2)
}

func TestCreateWorkflow_StoresCanonicalScopeModel(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		wantModel    string
		wantScopeKey string
	}{
		{
			name: "trimmed model",
			body: `{
				"scope_provider_name":"openai",
				"scope_model":"  gpt-5  ",
				"name":"trimmed model",
				"workflow_payload":{
					"schema_version":1,
					"features":{"cache":true,"audit":true,"usage":true,"guardrails":false},
					"guardrails":[]
				}
			}`,
			wantModel:    "gpt-5",
			wantScopeKey: "provider_model:openai:gpt-5",
		},
		{
			name: "whitespace only model keeps provider-only scope",
			body: `{
				"scope_provider_name":"openai",
				"scope_model":"   ",
				"name":"provider only",
				"workflow_payload":{
					"schema_version":1,
					"features":{"cache":true,"audit":true,"usage":true,"guardrails":false},
					"guardrails":[]
				}
			}`,
			wantModel:    "",
			wantScopeKey: "provider:openai",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &workflowTestStore{versions: []workflows.Version{globalWorkflow(workflows.FeatureFlags{Cache: true, Audit: true, Usage: true})}}

			h := newWorkflowHandler(t, store, nil)

			c, rec := echotest.Request(t, http.MethodPost, "/admin/workflows", tt.body)
			err := h.CreateWorkflow(c)
			require.NoError(t, err)
			require.Equal(t, http.StatusCreated, rec.Code)

			body := echotest.Decode[workflows.Version](t, rec)
			assert.Equal(t, tt.wantModel, body.Scope.Model)
			assert.Equal(t, tt.wantScopeKey, body.ScopeKey)
		})
	}
}

func TestCreateWorkflow_LegacyProviderTypeResolvesToConfiguredProviderName(t *testing.T) {
	store := &workflowTestStore{versions: []workflows.Version{globalWorkflow(workflows.FeatureFlags{Cache: true, Audit: true, Usage: true})}}

	modelRegistry := providers.NewModelRegistry()
	modelRegistry.RegisterProviderWithNameAndType(&handlerMockProvider{
		models: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-5", Object: "model", OwnedBy: "openai"},
			},
		},
	}, "primary-openai", "openai")
	err := modelRegistry.Initialize(context.Background())
	require.NoError(t, err)

	h := newWorkflowHandlerWithModelRegistry(t, store, modelRegistry, nil)

	c, rec := echotest.Request(t, http.MethodPost, "/admin/workflows", `{
		"scope_provider":"openai",
		"scope_model":"gpt-5",
		"name":"legacy provider type scope",
		"workflow_payload":{
			"schema_version":1,
			"features":{"cache":true,"audit":true,"usage":true,"guardrails":false},
			"guardrails":[]
		}
	}`)
	err = h.CreateWorkflow(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, rec.Code)

	body := echotest.Decode[workflows.Version](t, rec)
	assert.Equal(t, "primary-openai", body.Scope.Provider)
	assert.Equal(t, "gpt-5", body.Scope.Model)
}

func TestCreateWorkflow_AllowsEmptyName(t *testing.T) {
	store := &workflowTestStore{versions: []workflows.Version{globalWorkflow(workflows.FeatureFlags{Cache: true, Audit: true, Usage: true})}}

	h := newWorkflowHandler(t, store, nil)

	c, rec := echotest.Request(t, http.MethodPost, "/admin/workflows", `{
		"scope_provider":"openai",
		"scope_model":"gpt-5",
		"description":"provider-model workflow",
		"workflow_payload":{
			"schema_version":1,
			"features":{"cache":false,"audit":true,"usage":true,"guardrails":false},
			"guardrails":[]
		}
	}`)
	err := h.CreateWorkflow(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, rec.Code)

	body := echotest.Decode[workflows.Version](t, rec)
	assert.Empty(t, body.Name)
}

func TestCreateWorkflowRejectsUnknownGuardrail(t *testing.T) {
	store := &workflowTestStore{versions: []workflows.Version{globalWorkflow(workflows.FeatureFlags{Cache: true, Audit: true, Usage: true})}}
	registry := newWorkflowRegistry(t)
	h := newWorkflowHandler(t, store, registry)

	c, rec := echotest.Request(t, http.MethodPost, "/admin/workflows", `{
		"name":"guardrail workflow",
		"workflow_payload":{
			"schema_version":1,
			"features":{"cache":true,"audit":true,"usage":true,"guardrails":true},
			"guardrails":[{"ref":"missing-guardrail","step":10}]
		}
	}`)
	err := h.CreateWorkflow(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	body := echotest.Decode[workflowErrorEnvelope](t, rec)
	assert.Equal(t, "invalid_request_error", body.Error.Type)
	assert.Equal(t, "unknown guardrail ref: missing-guardrail", body.Error.Message)
	assert.Nil(t, body.Error.Param)
	assert.Nil(t, body.Error.Code)
}

func TestCreateWorkflowReturnsValidationErrors(t *testing.T) {
	store := &workflowTestStore{versions: []workflows.Version{globalWorkflow(workflows.FeatureFlags{Cache: true, Audit: true, Usage: true})}}

	h := newWorkflowHandler(t, store, nil)

	c, rec := echotest.Request(t, http.MethodPost, "/admin/workflows", `{
		"scope_model":"gpt-5",
		"name":"invalid scope",
		"workflow_payload":{
			"schema_version":1,
			"features":{"cache":true,"audit":true,"usage":true,"guardrails":false},
			"guardrails":[]
		}
	}`)
	err := h.CreateWorkflow(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	body := echotest.Decode[workflowErrorEnvelope](t, rec)
	assert.Equal(t, "invalid_request_error", body.Error.Type)
	assert.Nil(t, body.Error.Param)
	assert.Nil(t, body.Error.Code)
}

func TestCreateWorkflowRejectsUnknownProviderOrModelScope(t *testing.T) {
	store := &workflowTestStore{versions: []workflows.Version{globalWorkflow(workflows.FeatureFlags{Cache: true, Audit: true, Usage: true})}}

	tests := []struct {
		name        string
		body        string
		wantMessage string
	}{
		{
			name: "unknown provider",
			body: `{
				"scope_provider":"anthropic",
				"name":"invalid provider",
				"workflow_payload":{
					"schema_version":1,
					"features":{"cache":true,"audit":true,"usage":true,"guardrails":false},
					"guardrails":[]
				}
			}`,
			wantMessage: "unknown provider name: anthropic",
		},
		{
			name: "unknown model for provider",
			body: `{
				"scope_provider":"openai",
				"scope_model":"gpt-4o-mini",
				"name":"invalid model",
				"workflow_payload":{
					"schema_version":1,
					"features":{"cache":true,"audit":true,"usage":true,"guardrails":false},
					"guardrails":[]
				}
			}`,
			wantMessage: "unknown model for provider name openai: gpt-4o-mini",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newWorkflowHandler(t, store, nil)

			c, rec := echotest.Request(t, http.MethodPost, "/admin/workflows", tt.body)
			err := h.CreateWorkflow(c)
			require.NoError(t, err)
			require.Equal(t, http.StatusBadRequest, rec.Code)

			body := echotest.Decode[workflowErrorEnvelope](t, rec)
			assert.Equal(t, "invalid_request_error", body.Error.Type)
			assert.Equal(t, tt.wantMessage, body.Error.Message)
			assert.Nil(t, body.Error.Param)
			assert.Nil(t, body.Error.Code)
		})
	}
}

func TestCreateWorkflow_UsesScopeUserPathInValidationErrors(t *testing.T) {
	store := &workflowTestStore{versions: []workflows.Version{globalWorkflow(workflows.FeatureFlags{Cache: true, Audit: true, Usage: true})}}
	h := newWorkflowHandler(t, store, nil)

	c, rec := echotest.Request(t, http.MethodPost, "/admin/workflows", `{
		"scope_user_path":"/team/../alpha",
		"name":"invalid path",
		"workflow_payload":{
			"schema_version":1,
			"features":{"cache":true,"audit":true,"usage":true,"guardrails":false},
			"guardrails":[]
		}
	}`)
	err := h.CreateWorkflow(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	body := echotest.Decode[workflowErrorEnvelope](t, rec)
	assert.Equal(t, "invalid_request_error", body.Error.Type)
	assert.Equal(t, `invalid scope_user_path: user path cannot contain '.' or '..' segments`, body.Error.Message)
}

func TestWorkflowViewReflectsFeatureCaps(t *testing.T) {
	store := &workflowTestStore{versions: []workflows.Version{globalWorkflow(workflows.FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: true})}}

	service, err := workflows.NewService(store, workflows.NewCompilerWithFeatureCaps(nil, core.WorkflowFeatures{
		Cache:      false,
		Audit:      true,
		Usage:      true,
		Guardrails: false,
	}))
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	h := NewHandler(nil, nil, WithWorkflows(service))
	c, rec := echotest.Get(t, "/admin/workflows")
	err = h.ListWorkflows(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	body := echotest.Decode[[]workflows.View](t, rec)
	require.Len(t, body, 1)
	assert.False(t, body[0].EffectiveFeatures.Cache)
	assert.False(t, body[0].EffectiveFeatures.Guardrails)
}

func TestDeactivateWorkflow(t *testing.T) {
	store := &workflowTestStore{
		versions: []workflows.Version{
			globalWorkflow(workflows.FeatureFlags{Cache: true, Audit: true, Usage: true}),
			{
				ID:       "provider-workflow",
				Scope:    workflows.Scope{Provider: "openai"},
				ScopeKey: "provider:openai",
				Version:  1,
				Active:   true,
				Name:     "openai",
				Payload: workflows.Payload{
					SchemaVersion: 1,
					Features:      workflows.FeatureFlags{Cache: false, Audit: true, Usage: true, Guardrails: false},
				},
				WorkflowHash: "hash-provider",
			},
		},
	}

	h := newWorkflowHandler(t, store, nil)

	c, rec := echotest.Post(t, "/admin/workflows/provider-workflow/deactivate", nil, echotest.WithPathValue("id", "provider-workflow"))
	err := h.DeactivateWorkflow(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, rec.Code)

	views, err := h.workflows.ListViews(context.Background())
	require.NoError(t, err)
	require.Len(t, views, 1)
	assert.Equal(t, "global-workflow", views[0].ID)
}

func TestDeactivateWorkflowRejectsGlobalWorkflow(t *testing.T) {
	store := &workflowTestStore{versions: []workflows.Version{globalWorkflow(workflows.FeatureFlags{Cache: true, Audit: true, Usage: true})}}

	h := newWorkflowHandler(t, store, nil)

	c, rec := echotest.Post(t, "/admin/workflows/global-workflow/deactivate", nil, echotest.WithPathValue("id", "global-workflow"))
	err := h.DeactivateWorkflow(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	body := echotest.Decode[workflowErrorEnvelope](t, rec)
	assert.Equal(t, "invalid_request_error", body.Error.Type)
	assert.Equal(t, "cannot deactivate the global workflow", body.Error.Message)
	assert.Nil(t, body.Error.Param)
	assert.Nil(t, body.Error.Code)
}

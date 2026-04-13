package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
	"gomodel/internal/guardrails"
	"gomodel/internal/providers"
	"gomodel/internal/responsecache"
	"gomodel/internal/workflows"
)

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

func newWorkflowRegistry(t *testing.T) *guardrails.Registry {
	t.Helper()

	registry := guardrails.NewRegistry()
	rule, err := guardrails.NewSystemPromptGuardrail("policy-system", guardrails.SystemPromptInject, "be precise")
	if err != nil {
		t.Fatalf("NewSystemPromptGuardrail() error = %v", err)
	}
	if err := registry.Register(rule, responsecache.GuardrailRuleDescriptor{
		Type:    "system_prompt",
		Mode:    string(guardrails.SystemPromptInject),
		Content: "be precise",
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	return registry
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
	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	return registry
}

func decodeWorkflowErrorEnvelope(t *testing.T, body []byte) workflowErrorEnvelope {
	t.Helper()

	var envelope workflowErrorEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return envelope
}

func newWorkflowHandler(t *testing.T, store workflows.Store, registry *guardrails.Registry) *Handler {
	return newWorkflowHandlerWithModelRegistry(t, store, newWorkflowModelRegistry(t), registry)
}

func newWorkflowHandlerWithModelRegistry(t *testing.T, store workflows.Store, modelRegistry *providers.ModelRegistry, guardrailRegistry *guardrails.Registry) *Handler {
	t.Helper()

	service, err := workflows.NewService(store, workflows.NewCompiler(guardrailRegistry))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	return NewHandler(nil, modelRegistry, WithWorkflows(service), WithGuardrailsRegistry(guardrailRegistry))
}

func TestListWorkflows(t *testing.T) {
	fallbackDisabled := false
	store := &workflowTestStore{
		versions: []workflows.Version{
			{
				ID:       "global-workflow",
				Scope:    workflows.Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: workflows.Payload{
					SchemaVersion: 1,
					Features:      workflows.FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false, Fallback: &fallbackDisabled},
				},
				WorkflowHash: "hash-global",
			},
		},
	}

	h := newWorkflowHandler(t, store, nil)
	c, rec := newHandlerContext("/admin/api/v1/workflows")

	if err := h.ListWorkflows(c); err != nil {
		t.Fatalf("ListWorkflows() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body []workflows.View
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(body) != 1 {
		t.Fatalf("len(body) = %d, want 1", len(body))
	}
	if body[0].ScopeType != "global" {
		t.Fatalf("scope type = %q, want global", body[0].ScopeType)
	}
	if body[0].ScopeDisplay != "global" {
		t.Fatalf("scope display = %q, want global", body[0].ScopeDisplay)
	}
	if body[0].Payload.Features.Fallback == nil || *body[0].Payload.Features.Fallback {
		t.Fatalf("payload fallback = %v, want explicit false", body[0].Payload.Features.Fallback)
	}
	if !body[0].EffectiveFeatures.Cache || !body[0].EffectiveFeatures.Audit || !body[0].EffectiveFeatures.Usage {
		t.Fatalf("effective features = %+v, want cache/audit/usage enabled", body[0].EffectiveFeatures)
	}
	if body[0].EffectiveFeatures.Fallback {
		t.Fatalf("effective features = %+v, want fallback disabled", body[0].EffectiveFeatures)
	}
}

func TestWorkflowsEndpointsReturn503WhenServiceUnavailable(t *testing.T) {
	h := NewHandler(nil, nil)
	e := echo.New()

	listCtx, listRec := newHandlerContext("/admin/api/v1/workflows")
	if err := h.ListWorkflows(listCtx); err != nil {
		t.Fatalf("ListWorkflows() error = %v", err)
	}
	if listRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("list status = %d, want 503", listRec.Code)
	}
	listEnvelope := decodeWorkflowErrorEnvelope(t, listRec.Body.Bytes())
	if listEnvelope.Error.Type != "invalid_request_error" {
		t.Fatalf("list error type = %q, want invalid_request_error", listEnvelope.Error.Type)
	}
	if listEnvelope.Error.Message != "workflows feature is unavailable" {
		t.Fatalf("list error message = %q, want workflows feature is unavailable", listEnvelope.Error.Message)
	}
	if listEnvelope.Error.Param != nil {
		t.Fatalf("list error param = %v, want nil", *listEnvelope.Error.Param)
	}
	if listEnvelope.Error.Code == nil || *listEnvelope.Error.Code != "feature_unavailable" {
		t.Fatalf("list error code = %v, want feature_unavailable", listEnvelope.Error.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/api/v1/workflows", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if err := h.CreateWorkflow(c); err != nil {
		t.Fatalf("CreateWorkflow() error = %v", err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("create status = %d, want 503", rec.Code)
	}
	createEnvelope := decodeWorkflowErrorEnvelope(t, rec.Body.Bytes())
	if createEnvelope.Error.Type != "invalid_request_error" {
		t.Fatalf("create error type = %q, want invalid_request_error", createEnvelope.Error.Type)
	}
	if createEnvelope.Error.Message != "workflows feature is unavailable" {
		t.Fatalf("create error message = %q, want workflows feature is unavailable", createEnvelope.Error.Message)
	}
	if createEnvelope.Error.Param != nil {
		t.Fatalf("create error param = %v, want nil", *createEnvelope.Error.Param)
	}
	if createEnvelope.Error.Code == nil || *createEnvelope.Error.Code != "feature_unavailable" {
		t.Fatalf("create error code = %v, want feature_unavailable", createEnvelope.Error.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/admin/api/v1/workflows/test-workflow/deactivate", nil)
	rec = httptest.NewRecorder()
	c = e.NewContext(req, rec)
	c.SetPath("/admin/api/v1/workflows/:id/deactivate")
	c.SetPathValues(echo.PathValues{{Name: "id", Value: "test-workflow"}})
	if err := h.DeactivateWorkflow(c); err != nil {
		t.Fatalf("DeactivateWorkflow() error = %v", err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("deactivate status = %d, want 503", rec.Code)
	}
	deactivateEnvelope := decodeWorkflowErrorEnvelope(t, rec.Body.Bytes())
	if deactivateEnvelope.Error.Type != "invalid_request_error" {
		t.Fatalf("deactivate error type = %q, want invalid_request_error", deactivateEnvelope.Error.Type)
	}
	if deactivateEnvelope.Error.Message != "workflows feature is unavailable" {
		t.Fatalf("deactivate error message = %q, want workflows feature is unavailable", deactivateEnvelope.Error.Message)
	}
	if deactivateEnvelope.Error.Param != nil {
		t.Fatalf("deactivate error param = %v, want nil", *deactivateEnvelope.Error.Param)
	}
	if deactivateEnvelope.Error.Code == nil || *deactivateEnvelope.Error.Code != "feature_unavailable" {
		t.Fatalf("deactivate error code = %v, want feature_unavailable", deactivateEnvelope.Error.Code)
	}

	getCtx, getRec := newHandlerContext("/admin/api/v1/workflows/test-workflow")
	getCtx.SetPath("/admin/api/v1/workflows/:id")
	getCtx.SetPathValues(echo.PathValues{{Name: "id", Value: "test-workflow"}})
	if err := h.GetWorkflow(getCtx); err != nil {
		t.Fatalf("GetWorkflow() error = %v", err)
	}
	if getRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("get status = %d, want 503", getRec.Code)
	}
	getEnvelope := decodeWorkflowErrorEnvelope(t, getRec.Body.Bytes())
	if getEnvelope.Error.Type != "invalid_request_error" {
		t.Fatalf("get error type = %q, want invalid_request_error", getEnvelope.Error.Type)
	}
	if getEnvelope.Error.Message != "workflows feature is unavailable" {
		t.Fatalf("get error message = %q, want workflows feature is unavailable", getEnvelope.Error.Message)
	}
	if getEnvelope.Error.Code == nil || *getEnvelope.Error.Code != "feature_unavailable" {
		t.Fatalf("get error code = %v, want feature_unavailable", getEnvelope.Error.Code)
	}
}

func TestGetWorkflow(t *testing.T) {
	fallbackEnabled := true
	store := &workflowTestStore{
		versions: []workflows.Version{
			{
				ID:       "global-workflow",
				Scope:    workflows.Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: workflows.Payload{
					SchemaVersion: 1,
					Features: workflows.FeatureFlags{
						Cache: true,
						Audit: true,
						Usage: true,
					},
				},
				WorkflowHash: "hash-global",
			},
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
						Fallback:   &fallbackEnabled,
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
	c, rec := newHandlerContext("/admin/api/v1/workflows/provider-workflow-v1")
	c.SetPath("/admin/api/v1/workflows/:id")
	c.SetPathValues(echo.PathValues{{Name: "id", Value: "provider-workflow-v1"}})

	if err := h.GetWorkflow(c); err != nil {
		t.Fatalf("GetWorkflow() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var rawBody map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &rawBody); err != nil {
		t.Fatalf("unmarshal raw response: %v", err)
	}
	var effectiveFeatures map[string]bool
	if err := json.Unmarshal(rawBody["effective_features"], &effectiveFeatures); err != nil {
		t.Fatalf("unmarshal effective_features: %v", err)
	}
	for _, key := range []string{"cache", "audit", "usage", "guardrails", "fallback"} {
		if _, ok := effectiveFeatures[key]; !ok {
			t.Fatalf("effective_features missing lower-case key %q: %s", key, rec.Body.String())
		}
	}
	if _, ok := effectiveFeatures["Cache"]; ok {
		t.Fatalf("effective_features leaked Go field key %q: %s", "Cache", rec.Body.String())
	}

	var body workflows.View
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if body.ID != "provider-workflow-v1" {
		t.Fatalf("id = %q, want provider-workflow-v1", body.ID)
	}
	if body.Active {
		t.Fatal("Active = true, want false")
	}
	if body.ScopeType != "provider_model" {
		t.Fatalf("scope type = %q, want provider_model", body.ScopeType)
	}
	if body.ScopeDisplay != "openai/gpt-5" {
		t.Fatalf("scope display = %q, want openai/gpt-5", body.ScopeDisplay)
	}
	if !body.Payload.Features.Usage || !body.Payload.Features.Audit || !body.Payload.Features.Guardrails {
		t.Fatalf("payload features = %+v, want usage/audit/guardrails enabled", body.Payload.Features)
	}
}

func TestCreateWorkflow_NormalizesScopeUserPath(t *testing.T) {
	store := &workflowTestStore{
		versions: []workflows.Version{
			{
				ID:       "global-workflow",
				Scope:    workflows.Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: workflows.Payload{
					SchemaVersion: 1,
					Features:      workflows.FeatureFlags{Cache: true, Audit: true, Usage: true},
				},
				WorkflowHash: "hash-global",
			},
		},
	}
	h := newWorkflowHandler(t, store, nil)
	e := echo.New()

	req := httptest.NewRequest(http.MethodPost, "/admin/api/v1/workflows", bytes.NewBufferString(`{
		"scope_provider_name":"openai",
		"scope_model":"gpt-5",
		"scope_user_path":" team//alpha/user/ ",
		"name":"Scoped workflow",
		"workflow_payload":{
			"schema_version":1,
			"features":{"cache":true,"audit":true,"usage":true,"guardrails":false}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.CreateWorkflow(c); err != nil {
		t.Fatalf("CreateWorkflow() error = %v", err)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}

	var version workflows.Version
	if err := json.Unmarshal(rec.Body.Bytes(), &version); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got := version.Scope.UserPath; got != "/team/alpha/user" {
		t.Fatalf("Scope.UserPath = %q, want /team/alpha/user", got)
	}
}

func TestListWorkflowGuardrails(t *testing.T) {
	registry := newWorkflowRegistry(t)
	h := NewHandler(nil, nil, WithGuardrailsRegistry(registry))
	c, rec := newHandlerContext("/admin/api/v1/workflows/guardrails")

	if err := h.ListWorkflowGuardrails(c); err != nil {
		t.Fatalf("ListWorkflowGuardrails() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body []string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(body) != 1 || body[0] != "policy-system" {
		t.Fatalf("body = %#v, want [policy-system]", body)
	}
}

func TestCreateWorkflow(t *testing.T) {
	store := &workflowTestStore{
		versions: []workflows.Version{
			{
				ID:       "global-workflow",
				Scope:    workflows.Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: workflows.Payload{
					SchemaVersion: 1,
					Features:      workflows.FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
				},
				WorkflowHash: "hash-global",
			},
		},
	}

	h := newWorkflowHandler(t, store, nil)
	e := echo.New()

	req := httptest.NewRequest(http.MethodPost, "/admin/api/v1/workflows", bytes.NewBufferString(`{
		"scope_provider":"openai",
		"scope_model":"gpt-5",
		"name":"openai gpt-5",
		"description":"provider-model workflow",
		"workflow_payload":{
			"schema_version":1,
			"features":{"cache":false,"audit":true,"usage":true,"guardrails":false,"fallback":false},
			"guardrails":[]
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.CreateWorkflow(c); err != nil {
		t.Fatalf("CreateWorkflow() error = %v", err)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}

	var body workflows.Version
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if body.Scope.Provider != "openai" || body.Scope.Model != "gpt-5" {
		t.Fatalf("scope = %#v, want openai/gpt-5", body.Scope)
	}
	if body.Name != "openai gpt-5" {
		t.Fatalf("name = %q, want openai gpt-5", body.Name)
	}
	if body.Payload.Features.Fallback == nil || *body.Payload.Features.Fallback {
		t.Fatalf("payload fallback = %v, want explicit false", body.Payload.Features.Fallback)
	}

	views, err := h.workflows.ListViews(context.Background())
	if err != nil {
		t.Fatalf("ListViews() error = %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("len(views) = %d, want 2", len(views))
	}
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
			store := &workflowTestStore{
				versions: []workflows.Version{
					{
						ID:       "global-workflow",
						Scope:    workflows.Scope{},
						ScopeKey: "global",
						Version:  1,
						Active:   true,
						Name:     "global",
						Payload: workflows.Payload{
							SchemaVersion: 1,
							Features:      workflows.FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
						},
						WorkflowHash: "hash-global",
					},
				},
			}

			h := newWorkflowHandler(t, store, nil)
			e := echo.New()

			req := httptest.NewRequest(http.MethodPost, "/admin/api/v1/workflows", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			if err := h.CreateWorkflow(c); err != nil {
				t.Fatalf("CreateWorkflow() error = %v", err)
			}
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201", rec.Code)
			}

			var body workflows.Version
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			if body.Scope.Model != tt.wantModel {
				t.Fatalf("Scope.Model = %q, want %q", body.Scope.Model, tt.wantModel)
			}
			if body.ScopeKey != tt.wantScopeKey {
				t.Fatalf("ScopeKey = %q, want %q", body.ScopeKey, tt.wantScopeKey)
			}
		})
	}
}

func TestCreateWorkflow_LegacyProviderTypeResolvesToConfiguredProviderName(t *testing.T) {
	store := &workflowTestStore{
		versions: []workflows.Version{
			{
				ID:       "global-workflow",
				Scope:    workflows.Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: workflows.Payload{
					SchemaVersion: 1,
					Features:      workflows.FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
				},
				WorkflowHash: "hash-global",
			},
		},
	}

	modelRegistry := providers.NewModelRegistry()
	modelRegistry.RegisterProviderWithNameAndType(&handlerMockProvider{
		models: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-5", Object: "model", OwnedBy: "openai"},
			},
		},
	}, "primary-openai", "openai")
	if err := modelRegistry.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	h := newWorkflowHandlerWithModelRegistry(t, store, modelRegistry, nil)
	e := echo.New()

	req := httptest.NewRequest(http.MethodPost, "/admin/api/v1/workflows", bytes.NewBufferString(`{
		"scope_provider":"openai",
		"scope_model":"gpt-5",
		"name":"legacy provider type scope",
		"workflow_payload":{
			"schema_version":1,
			"features":{"cache":true,"audit":true,"usage":true,"guardrails":false},
			"guardrails":[]
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.CreateWorkflow(c); err != nil {
		t.Fatalf("CreateWorkflow() error = %v", err)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}

	var body workflows.Version
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if body.Scope.Provider != "primary-openai" || body.Scope.Model != "gpt-5" {
		t.Fatalf("scope = %#v, want primary-openai/gpt-5", body.Scope)
	}
}

func TestCreateWorkflow_AllowsEmptyName(t *testing.T) {
	store := &workflowTestStore{
		versions: []workflows.Version{
			{
				ID:       "global-workflow",
				Scope:    workflows.Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "",
				Payload: workflows.Payload{
					SchemaVersion: 1,
					Features:      workflows.FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
				},
				WorkflowHash: "hash-global",
			},
		},
	}

	h := newWorkflowHandler(t, store, nil)
	e := echo.New()

	req := httptest.NewRequest(http.MethodPost, "/admin/api/v1/workflows", bytes.NewBufferString(`{
		"scope_provider":"openai",
		"scope_model":"gpt-5",
		"description":"provider-model workflow",
		"workflow_payload":{
			"schema_version":1,
			"features":{"cache":false,"audit":true,"usage":true,"guardrails":false},
			"guardrails":[]
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.CreateWorkflow(c); err != nil {
		t.Fatalf("CreateWorkflow() error = %v", err)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}

	var body workflows.Version
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if body.Name != "" {
		t.Fatalf("name = %q, want empty", body.Name)
	}
}

func TestCreateWorkflowRejectsUnknownGuardrail(t *testing.T) {
	store := &workflowTestStore{
		versions: []workflows.Version{
			{
				ID:       "global-workflow",
				Scope:    workflows.Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: workflows.Payload{
					SchemaVersion: 1,
					Features:      workflows.FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
				},
				WorkflowHash: "hash-global",
			},
		},
	}
	registry := newWorkflowRegistry(t)
	h := newWorkflowHandler(t, store, registry)
	e := echo.New()

	req := httptest.NewRequest(http.MethodPost, "/admin/api/v1/workflows", bytes.NewBufferString(`{
		"name":"guardrail workflow",
		"workflow_payload":{
			"schema_version":1,
			"features":{"cache":true,"audit":true,"usage":true,"guardrails":true},
			"guardrails":[{"ref":"missing-guardrail","step":10}]
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.CreateWorkflow(c); err != nil {
		t.Fatalf("CreateWorkflow() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}

	body := decodeWorkflowErrorEnvelope(t, rec.Body.Bytes())
	if body.Error.Type != "invalid_request_error" {
		t.Fatalf("error type = %q, want invalid_request_error", body.Error.Type)
	}
	if body.Error.Message != "unknown guardrail ref: missing-guardrail" {
		t.Fatalf("error message = %q, want unknown guardrail ref", body.Error.Message)
	}
	if body.Error.Param != nil {
		t.Fatalf("error param = %v, want nil", *body.Error.Param)
	}
	if body.Error.Code != nil {
		t.Fatalf("error code = %v, want nil", *body.Error.Code)
	}
}

func TestCreateWorkflowReturnsValidationErrors(t *testing.T) {
	store := &workflowTestStore{
		versions: []workflows.Version{
			{
				ID:       "global-workflow",
				Scope:    workflows.Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: workflows.Payload{
					SchemaVersion: 1,
					Features:      workflows.FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
				},
				WorkflowHash: "hash-global",
			},
		},
	}

	h := newWorkflowHandler(t, store, nil)
	e := echo.New()

	req := httptest.NewRequest(http.MethodPost, "/admin/api/v1/workflows", bytes.NewBufferString(`{
		"scope_model":"gpt-5",
		"name":"invalid scope",
		"workflow_payload":{
			"schema_version":1,
			"features":{"cache":true,"audit":true,"usage":true,"guardrails":false},
			"guardrails":[]
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.CreateWorkflow(c); err != nil {
		t.Fatalf("CreateWorkflow() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}

	body := decodeWorkflowErrorEnvelope(t, rec.Body.Bytes())
	if body.Error.Type != "invalid_request_error" {
		t.Fatalf("error type = %q, want invalid_request_error", body.Error.Type)
	}
	if body.Error.Param != nil {
		t.Fatalf("error param = %v, want nil", *body.Error.Param)
	}
	if body.Error.Code != nil {
		t.Fatalf("error code = %v, want nil", *body.Error.Code)
	}
}

func TestCreateWorkflowRejectsUnknownProviderOrModelScope(t *testing.T) {
	store := &workflowTestStore{
		versions: []workflows.Version{
			{
				ID:       "global-workflow",
				Scope:    workflows.Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: workflows.Payload{
					SchemaVersion: 1,
					Features:      workflows.FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
				},
				WorkflowHash: "hash-global",
			},
		},
	}

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
			e := echo.New()

			req := httptest.NewRequest(http.MethodPost, "/admin/api/v1/workflows", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			if err := h.CreateWorkflow(c); err != nil {
				t.Fatalf("CreateWorkflow() error = %v", err)
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}

			body := decodeWorkflowErrorEnvelope(t, rec.Body.Bytes())
			if body.Error.Type != "invalid_request_error" {
				t.Fatalf("error type = %q, want invalid_request_error", body.Error.Type)
			}
			if body.Error.Message != tt.wantMessage {
				t.Fatalf("error message = %q, want %q", body.Error.Message, tt.wantMessage)
			}
			if body.Error.Param != nil {
				t.Fatalf("error param = %v, want nil", *body.Error.Param)
			}
			if body.Error.Code != nil {
				t.Fatalf("error code = %v, want nil", *body.Error.Code)
			}
		})
	}
}

func TestCreateWorkflow_UsesScopeUserPathInValidationErrors(t *testing.T) {
	store := &workflowTestStore{
		versions: []workflows.Version{
			{
				ID:       "global-workflow",
				Scope:    workflows.Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: workflows.Payload{
					SchemaVersion: 1,
					Features:      workflows.FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
				},
				WorkflowHash: "hash-global",
			},
		},
	}
	h := newWorkflowHandler(t, store, nil)
	e := echo.New()

	req := httptest.NewRequest(http.MethodPost, "/admin/api/v1/workflows", bytes.NewBufferString(`{
		"scope_user_path":"/team/../alpha",
		"name":"invalid path",
		"workflow_payload":{
			"schema_version":1,
			"features":{"cache":true,"audit":true,"usage":true,"guardrails":false},
			"guardrails":[]
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.CreateWorkflow(c); err != nil {
		t.Fatalf("CreateWorkflow() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}

	body := decodeWorkflowErrorEnvelope(t, rec.Body.Bytes())
	if body.Error.Type != "invalid_request_error" {
		t.Fatalf("error type = %q, want invalid_request_error", body.Error.Type)
	}
	if body.Error.Message != `invalid scope_user_path: user path cannot contain '.' or '..' segments` {
		t.Fatalf("error message = %q, want invalid scope_user_path message", body.Error.Message)
	}
}

func TestWorkflowViewReflectsFeatureCaps(t *testing.T) {
	store := &workflowTestStore{
		versions: []workflows.Version{
			{
				ID:       "global-workflow",
				Scope:    workflows.Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: workflows.Payload{
					SchemaVersion: 1,
					Features:      workflows.FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: true},
				},
				WorkflowHash: "hash-global",
			},
		},
	}

	service, err := workflows.NewService(store, workflows.NewCompilerWithFeatureCaps(nil, core.WorkflowFeatures{
		Cache:      false,
		Audit:      true,
		Usage:      true,
		Guardrails: false,
	}))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	h := NewHandler(nil, nil, WithWorkflows(service))
	c, rec := newHandlerContext("/admin/api/v1/workflows")

	if err := h.ListWorkflows(c); err != nil {
		t.Fatalf("ListWorkflows() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body []workflows.View
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(body) != 1 {
		t.Fatalf("len(body) = %d, want 1", len(body))
	}
	if body[0].EffectiveFeatures.Cache {
		t.Fatal("effective cache feature = true, want false")
	}
	if body[0].EffectiveFeatures.Guardrails {
		t.Fatal("effective guardrails feature = true, want false")
	}
}

func TestDeactivateWorkflow(t *testing.T) {
	store := &workflowTestStore{
		versions: []workflows.Version{
			{
				ID:       "global-workflow",
				Scope:    workflows.Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: workflows.Payload{
					SchemaVersion: 1,
					Features:      workflows.FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
				},
				WorkflowHash: "hash-global",
			},
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
	e := echo.New()

	req := httptest.NewRequest(http.MethodPost, "/admin/api/v1/workflows/provider-workflow/deactivate", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/admin/api/v1/workflows/:id/deactivate")
	c.SetPathValues(echo.PathValues{{Name: "id", Value: "provider-workflow"}})

	if err := h.DeactivateWorkflow(c); err != nil {
		t.Fatalf("DeactivateWorkflow() error = %v", err)
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}

	views, err := h.workflows.ListViews(context.Background())
	if err != nil {
		t.Fatalf("ListViews() error = %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("len(views) = %d, want 1", len(views))
	}
	if views[0].ID != "global-workflow" {
		t.Fatalf("remaining view = %q, want global-workflow", views[0].ID)
	}
}

func TestDeactivateWorkflowRejectsGlobalWorkflow(t *testing.T) {
	store := &workflowTestStore{
		versions: []workflows.Version{
			{
				ID:       "global-workflow",
				Scope:    workflows.Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: workflows.Payload{
					SchemaVersion: 1,
					Features:      workflows.FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
				},
				WorkflowHash: "hash-global",
			},
		},
	}

	h := newWorkflowHandler(t, store, nil)
	e := echo.New()

	req := httptest.NewRequest(http.MethodPost, "/admin/api/v1/workflows/global-workflow/deactivate", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/admin/api/v1/workflows/:id/deactivate")
	c.SetPathValues(echo.PathValues{{Name: "id", Value: "global-workflow"}})

	if err := h.DeactivateWorkflow(c); err != nil {
		t.Fatalf("DeactivateWorkflow() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}

	body := decodeWorkflowErrorEnvelope(t, rec.Body.Bytes())
	if body.Error.Type != "invalid_request_error" {
		t.Fatalf("error type = %q, want invalid_request_error", body.Error.Type)
	}
	if body.Error.Message != "cannot deactivate the global workflow" {
		t.Fatalf("error message = %q, want cannot deactivate the global workflow", body.Error.Message)
	}
	if body.Error.Param != nil {
		t.Fatalf("error param = %v, want nil", *body.Error.Param)
	}
	if body.Error.Code != nil {
		t.Fatalf("error code = %v, want nil", *body.Error.Code)
	}
}

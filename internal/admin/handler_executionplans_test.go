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
	"gomodel/internal/executionplans"
	"gomodel/internal/guardrails"
	"gomodel/internal/providers"
	"gomodel/internal/responsecache"
)

type executionPlanTestStore struct {
	versions []executionplans.Version
}

type executionPlanErrorEnvelope struct {
	Error struct {
		Type    string  `json:"type"`
		Message string  `json:"message"`
		Param   *string `json:"param"`
		Code    *string `json:"code"`
	} `json:"error"`
}

func (s *executionPlanTestStore) ListActive(context.Context) ([]executionplans.Version, error) {
	result := make([]executionplans.Version, 0, len(s.versions))
	for _, version := range s.versions {
		if version.Active {
			result = append(result, version)
		}
	}
	return result, nil
}

func (s *executionPlanTestStore) Get(_ context.Context, id string) (*executionplans.Version, error) {
	for _, version := range s.versions {
		if version.ID == id {
			copy := version
			return &copy, nil
		}
	}
	return nil, executionplans.ErrNotFound
}

func (s *executionPlanTestStore) Create(_ context.Context, input executionplans.CreateInput) (*executionplans.Version, error) {
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
	planHash, err := executionPlanTestPlanHash(input.Payload)
	if err != nil {
		return nil, err
	}

	version := executionplans.Version{
		ID:          "plan-created",
		Scope:       input.Scope,
		ScopeKey:    scopeKey,
		Version:     len(s.versions) + 1,
		Active:      input.Activate,
		Name:        input.Name,
		Description: input.Description,
		Payload:     input.Payload,
		PlanHash:    planHash,
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

func (s *executionPlanTestStore) EnsureManagedDefaultGlobal(ctx context.Context, input executionplans.CreateInput, planHash string) (*executionplans.Version, error) {
	for _, version := range s.versions {
		if !version.Active || version.ScopeKey != "global" {
			continue
		}
		if !version.Managed {
			return nil, nil
		}
		if version.Name == input.Name && version.Description == input.Description && version.PlanHash == planHash {
			return nil, nil
		}
		break
	}
	return s.Create(ctx, input)
}

func (s *executionPlanTestStore) Deactivate(_ context.Context, id string) error {
	for i := range s.versions {
		if s.versions[i].ID == id && s.versions[i].Active {
			s.versions[i].Active = false
			return nil
		}
	}
	return executionplans.ErrNotFound
}

func (s *executionPlanTestStore) Close() error { return nil }

func executionPlanTestPlanHash(payload executionplans.Payload) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func newExecutionPlanRegistry(t *testing.T) *guardrails.Registry {
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

func newExecutionPlanModelRegistry(t *testing.T) *providers.ModelRegistry {
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

func decodeExecutionPlanErrorEnvelope(t *testing.T, body []byte) executionPlanErrorEnvelope {
	t.Helper()

	var envelope executionPlanErrorEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return envelope
}

func newExecutionPlanHandler(t *testing.T, store executionplans.Store, registry *guardrails.Registry) *Handler {
	return newExecutionPlanHandlerWithModelRegistry(t, store, newExecutionPlanModelRegistry(t), registry)
}

func newExecutionPlanHandlerWithModelRegistry(t *testing.T, store executionplans.Store, modelRegistry *providers.ModelRegistry, guardrailRegistry *guardrails.Registry) *Handler {
	t.Helper()

	service, err := executionplans.NewService(store, executionplans.NewCompiler(guardrailRegistry))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	return NewHandler(nil, modelRegistry, WithExecutionPlans(service), WithGuardrailsRegistry(guardrailRegistry))
}

func TestListExecutionPlans(t *testing.T) {
	fallbackDisabled := false
	store := &executionPlanTestStore{
		versions: []executionplans.Version{
			{
				ID:       "global-plan",
				Scope:    executionplans.Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: executionplans.Payload{
					SchemaVersion: 1,
					Features:      executionplans.FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false, Fallback: &fallbackDisabled},
				},
				PlanHash: "hash-global",
			},
		},
	}

	h := newExecutionPlanHandler(t, store, nil)
	c, rec := newHandlerContext("/admin/api/v1/execution-plans")

	if err := h.ListExecutionPlans(c); err != nil {
		t.Fatalf("ListExecutionPlans() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body []executionplans.View
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

func TestExecutionPlansEndpointsReturn503WhenServiceUnavailable(t *testing.T) {
	h := NewHandler(nil, nil)
	e := echo.New()

	listCtx, listRec := newHandlerContext("/admin/api/v1/execution-plans")
	if err := h.ListExecutionPlans(listCtx); err != nil {
		t.Fatalf("ListExecutionPlans() error = %v", err)
	}
	if listRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("list status = %d, want 503", listRec.Code)
	}
	listEnvelope := decodeExecutionPlanErrorEnvelope(t, listRec.Body.Bytes())
	if listEnvelope.Error.Type != "invalid_request_error" {
		t.Fatalf("list error type = %q, want invalid_request_error", listEnvelope.Error.Type)
	}
	if listEnvelope.Error.Message != "execution plans feature is unavailable" {
		t.Fatalf("list error message = %q, want execution plans feature is unavailable", listEnvelope.Error.Message)
	}
	if listEnvelope.Error.Param != nil {
		t.Fatalf("list error param = %v, want nil", *listEnvelope.Error.Param)
	}
	if listEnvelope.Error.Code == nil || *listEnvelope.Error.Code != "feature_unavailable" {
		t.Fatalf("list error code = %v, want feature_unavailable", listEnvelope.Error.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/api/v1/execution-plans", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if err := h.CreateExecutionPlan(c); err != nil {
		t.Fatalf("CreateExecutionPlan() error = %v", err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("create status = %d, want 503", rec.Code)
	}
	createEnvelope := decodeExecutionPlanErrorEnvelope(t, rec.Body.Bytes())
	if createEnvelope.Error.Type != "invalid_request_error" {
		t.Fatalf("create error type = %q, want invalid_request_error", createEnvelope.Error.Type)
	}
	if createEnvelope.Error.Message != "execution plans feature is unavailable" {
		t.Fatalf("create error message = %q, want execution plans feature is unavailable", createEnvelope.Error.Message)
	}
	if createEnvelope.Error.Param != nil {
		t.Fatalf("create error param = %v, want nil", *createEnvelope.Error.Param)
	}
	if createEnvelope.Error.Code == nil || *createEnvelope.Error.Code != "feature_unavailable" {
		t.Fatalf("create error code = %v, want feature_unavailable", createEnvelope.Error.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/admin/api/v1/execution-plans/test-plan/deactivate", nil)
	rec = httptest.NewRecorder()
	c = e.NewContext(req, rec)
	c.SetPath("/admin/api/v1/execution-plans/:id/deactivate")
	c.SetPathValues(echo.PathValues{{Name: "id", Value: "test-plan"}})
	if err := h.DeactivateExecutionPlan(c); err != nil {
		t.Fatalf("DeactivateExecutionPlan() error = %v", err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("deactivate status = %d, want 503", rec.Code)
	}
	deactivateEnvelope := decodeExecutionPlanErrorEnvelope(t, rec.Body.Bytes())
	if deactivateEnvelope.Error.Type != "invalid_request_error" {
		t.Fatalf("deactivate error type = %q, want invalid_request_error", deactivateEnvelope.Error.Type)
	}
	if deactivateEnvelope.Error.Message != "execution plans feature is unavailable" {
		t.Fatalf("deactivate error message = %q, want execution plans feature is unavailable", deactivateEnvelope.Error.Message)
	}
	if deactivateEnvelope.Error.Param != nil {
		t.Fatalf("deactivate error param = %v, want nil", *deactivateEnvelope.Error.Param)
	}
	if deactivateEnvelope.Error.Code == nil || *deactivateEnvelope.Error.Code != "feature_unavailable" {
		t.Fatalf("deactivate error code = %v, want feature_unavailable", deactivateEnvelope.Error.Code)
	}

	getCtx, getRec := newHandlerContext("/admin/api/v1/execution-plans/test-plan")
	getCtx.SetPath("/admin/api/v1/execution-plans/:id")
	getCtx.SetPathValues(echo.PathValues{{Name: "id", Value: "test-plan"}})
	if err := h.GetExecutionPlan(getCtx); err != nil {
		t.Fatalf("GetExecutionPlan() error = %v", err)
	}
	if getRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("get status = %d, want 503", getRec.Code)
	}
	getEnvelope := decodeExecutionPlanErrorEnvelope(t, getRec.Body.Bytes())
	if getEnvelope.Error.Type != "invalid_request_error" {
		t.Fatalf("get error type = %q, want invalid_request_error", getEnvelope.Error.Type)
	}
	if getEnvelope.Error.Message != "execution plans feature is unavailable" {
		t.Fatalf("get error message = %q, want execution plans feature is unavailable", getEnvelope.Error.Message)
	}
	if getEnvelope.Error.Code == nil || *getEnvelope.Error.Code != "feature_unavailable" {
		t.Fatalf("get error code = %v, want feature_unavailable", getEnvelope.Error.Code)
	}
}

func TestGetExecutionPlan(t *testing.T) {
	fallbackEnabled := true
	store := &executionPlanTestStore{
		versions: []executionplans.Version{
			{
				ID:       "global-plan",
				Scope:    executionplans.Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: executionplans.Payload{
					SchemaVersion: 1,
					Features: executionplans.FeatureFlags{
						Cache: true,
						Audit: true,
						Usage: true,
					},
				},
				PlanHash: "hash-global",
			},
			{
				ID:          "provider-plan-v1",
				Scope:       executionplans.Scope{Provider: "openai", Model: "gpt-5"},
				ScopeKey:    "provider_model:openai:gpt-5",
				Version:     1,
				Active:      false,
				Name:        "historical provider workflow",
				Description: "inactive but still queryable",
				Payload: executionplans.Payload{
					SchemaVersion: 1,
					Features: executionplans.FeatureFlags{
						Cache:      true,
						Audit:      true,
						Usage:      true,
						Guardrails: true,
						Fallback:   &fallbackEnabled,
					},
					Guardrails: []executionplans.GuardrailStep{
						{Ref: "policy-system", Step: 10},
					},
				},
				PlanHash: "hash-provider-v1",
			},
		},
	}

	registry := newExecutionPlanRegistry(t)
	h := newExecutionPlanHandler(t, store, registry)
	c, rec := newHandlerContext("/admin/api/v1/execution-plans/provider-plan-v1")
	c.SetPath("/admin/api/v1/execution-plans/:id")
	c.SetPathValues(echo.PathValues{{Name: "id", Value: "provider-plan-v1"}})

	if err := h.GetExecutionPlan(c); err != nil {
		t.Fatalf("GetExecutionPlan() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body executionplans.View
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if body.ID != "provider-plan-v1" {
		t.Fatalf("id = %q, want provider-plan-v1", body.ID)
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

func TestCreateExecutionPlan_NormalizesScopeUserPath(t *testing.T) {
	store := &executionPlanTestStore{
		versions: []executionplans.Version{
			{
				ID:       "global-plan",
				Scope:    executionplans.Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: executionplans.Payload{
					SchemaVersion: 1,
					Features:      executionplans.FeatureFlags{Cache: true, Audit: true, Usage: true},
				},
				PlanHash: "hash-global",
			},
		},
	}
	h := newExecutionPlanHandler(t, store, nil)
	e := echo.New()

	req := httptest.NewRequest(http.MethodPost, "/admin/api/v1/execution-plans", bytes.NewBufferString(`{
		"scope_provider_name":"openai",
		"scope_model":"gpt-5",
		"scope_user_path":" team//alpha/user/ ",
		"name":"Scoped workflow",
		"plan_payload":{
			"schema_version":1,
			"features":{"cache":true,"audit":true,"usage":true,"guardrails":false}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.CreateExecutionPlan(c); err != nil {
		t.Fatalf("CreateExecutionPlan() error = %v", err)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}

	var version executionplans.Version
	if err := json.Unmarshal(rec.Body.Bytes(), &version); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got := version.Scope.UserPath; got != "/team/alpha/user" {
		t.Fatalf("Scope.UserPath = %q, want /team/alpha/user", got)
	}
}

func TestListExecutionPlanGuardrails(t *testing.T) {
	registry := newExecutionPlanRegistry(t)
	h := NewHandler(nil, nil, WithGuardrailsRegistry(registry))
	c, rec := newHandlerContext("/admin/api/v1/execution-plans/guardrails")

	if err := h.ListExecutionPlanGuardrails(c); err != nil {
		t.Fatalf("ListExecutionPlanGuardrails() error = %v", err)
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

func TestCreateExecutionPlan(t *testing.T) {
	store := &executionPlanTestStore{
		versions: []executionplans.Version{
			{
				ID:       "global-plan",
				Scope:    executionplans.Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: executionplans.Payload{
					SchemaVersion: 1,
					Features:      executionplans.FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
				},
				PlanHash: "hash-global",
			},
		},
	}

	h := newExecutionPlanHandler(t, store, nil)
	e := echo.New()

	req := httptest.NewRequest(http.MethodPost, "/admin/api/v1/execution-plans", bytes.NewBufferString(`{
		"scope_provider":"openai",
		"scope_model":"gpt-5",
		"name":"openai gpt-5",
		"description":"provider-model plan",
		"plan_payload":{
			"schema_version":1,
			"features":{"cache":false,"audit":true,"usage":true,"guardrails":false,"fallback":false},
			"guardrails":[]
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.CreateExecutionPlan(c); err != nil {
		t.Fatalf("CreateExecutionPlan() error = %v", err)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}

	var body executionplans.Version
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

	views, err := h.plans.ListViews(context.Background())
	if err != nil {
		t.Fatalf("ListViews() error = %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("len(views) = %d, want 2", len(views))
	}
}

func TestCreateExecutionPlan_AllowsEmptyName(t *testing.T) {
	store := &executionPlanTestStore{
		versions: []executionplans.Version{
			{
				ID:       "global-plan",
				Scope:    executionplans.Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "",
				Payload: executionplans.Payload{
					SchemaVersion: 1,
					Features:      executionplans.FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
				},
				PlanHash: "hash-global",
			},
		},
	}

	h := newExecutionPlanHandler(t, store, nil)
	e := echo.New()

	req := httptest.NewRequest(http.MethodPost, "/admin/api/v1/execution-plans", bytes.NewBufferString(`{
		"scope_provider":"openai",
		"scope_model":"gpt-5",
		"description":"provider-model plan",
		"plan_payload":{
			"schema_version":1,
			"features":{"cache":false,"audit":true,"usage":true,"guardrails":false},
			"guardrails":[]
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.CreateExecutionPlan(c); err != nil {
		t.Fatalf("CreateExecutionPlan() error = %v", err)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}

	var body executionplans.Version
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if body.Name != "" {
		t.Fatalf("name = %q, want empty", body.Name)
	}
}

func TestCreateExecutionPlanRejectsUnknownGuardrail(t *testing.T) {
	store := &executionPlanTestStore{
		versions: []executionplans.Version{
			{
				ID:       "global-plan",
				Scope:    executionplans.Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: executionplans.Payload{
					SchemaVersion: 1,
					Features:      executionplans.FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
				},
				PlanHash: "hash-global",
			},
		},
	}
	registry := newExecutionPlanRegistry(t)
	h := newExecutionPlanHandler(t, store, registry)
	e := echo.New()

	req := httptest.NewRequest(http.MethodPost, "/admin/api/v1/execution-plans", bytes.NewBufferString(`{
		"name":"guardrail plan",
		"plan_payload":{
			"schema_version":1,
			"features":{"cache":true,"audit":true,"usage":true,"guardrails":true},
			"guardrails":[{"ref":"missing-guardrail","step":10}]
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.CreateExecutionPlan(c); err != nil {
		t.Fatalf("CreateExecutionPlan() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}

	body := decodeExecutionPlanErrorEnvelope(t, rec.Body.Bytes())
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

func TestCreateExecutionPlanReturnsValidationErrors(t *testing.T) {
	store := &executionPlanTestStore{
		versions: []executionplans.Version{
			{
				ID:       "global-plan",
				Scope:    executionplans.Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: executionplans.Payload{
					SchemaVersion: 1,
					Features:      executionplans.FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
				},
				PlanHash: "hash-global",
			},
		},
	}

	h := newExecutionPlanHandler(t, store, nil)
	e := echo.New()

	req := httptest.NewRequest(http.MethodPost, "/admin/api/v1/execution-plans", bytes.NewBufferString(`{
		"scope_model":"gpt-5",
		"name":"invalid scope",
		"plan_payload":{
			"schema_version":1,
			"features":{"cache":true,"audit":true,"usage":true,"guardrails":false},
			"guardrails":[]
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.CreateExecutionPlan(c); err != nil {
		t.Fatalf("CreateExecutionPlan() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}

	body := decodeExecutionPlanErrorEnvelope(t, rec.Body.Bytes())
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

func TestCreateExecutionPlanRejectsUnknownProviderOrModelScope(t *testing.T) {
	store := &executionPlanTestStore{
		versions: []executionplans.Version{
			{
				ID:       "global-plan",
				Scope:    executionplans.Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: executionplans.Payload{
					SchemaVersion: 1,
					Features:      executionplans.FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
				},
				PlanHash: "hash-global",
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
				"plan_payload":{
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
				"plan_payload":{
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
			h := newExecutionPlanHandler(t, store, nil)
			e := echo.New()

			req := httptest.NewRequest(http.MethodPost, "/admin/api/v1/execution-plans", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			if err := h.CreateExecutionPlan(c); err != nil {
				t.Fatalf("CreateExecutionPlan() error = %v", err)
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}

			body := decodeExecutionPlanErrorEnvelope(t, rec.Body.Bytes())
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

func TestCreateExecutionPlan_UsesScopeUserPathInValidationErrors(t *testing.T) {
	store := &executionPlanTestStore{
		versions: []executionplans.Version{
			{
				ID:       "global-plan",
				Scope:    executionplans.Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: executionplans.Payload{
					SchemaVersion: 1,
					Features:      executionplans.FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
				},
				PlanHash: "hash-global",
			},
		},
	}
	h := newExecutionPlanHandler(t, store, nil)
	e := echo.New()

	req := httptest.NewRequest(http.MethodPost, "/admin/api/v1/execution-plans", bytes.NewBufferString(`{
		"scope_user_path":"/team/../alpha",
		"name":"invalid path",
		"plan_payload":{
			"schema_version":1,
			"features":{"cache":true,"audit":true,"usage":true,"guardrails":false},
			"guardrails":[]
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.CreateExecutionPlan(c); err != nil {
		t.Fatalf("CreateExecutionPlan() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}

	body := decodeExecutionPlanErrorEnvelope(t, rec.Body.Bytes())
	if body.Error.Type != "invalid_request_error" {
		t.Fatalf("error type = %q, want invalid_request_error", body.Error.Type)
	}
	if body.Error.Message != `invalid scope_user_path: user path cannot contain '.' or '..' segments` {
		t.Fatalf("error message = %q, want invalid scope_user_path message", body.Error.Message)
	}
}

func TestExecutionPlanViewReflectsFeatureCaps(t *testing.T) {
	store := &executionPlanTestStore{
		versions: []executionplans.Version{
			{
				ID:       "global-plan",
				Scope:    executionplans.Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: executionplans.Payload{
					SchemaVersion: 1,
					Features:      executionplans.FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: true},
				},
				PlanHash: "hash-global",
			},
		},
	}

	service, err := executionplans.NewService(store, executionplans.NewCompilerWithFeatureCaps(nil, core.ExecutionFeatures{
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

	h := NewHandler(nil, nil, WithExecutionPlans(service))
	c, rec := newHandlerContext("/admin/api/v1/execution-plans")

	if err := h.ListExecutionPlans(c); err != nil {
		t.Fatalf("ListExecutionPlans() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body []executionplans.View
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

func TestDeactivateExecutionPlan(t *testing.T) {
	store := &executionPlanTestStore{
		versions: []executionplans.Version{
			{
				ID:       "global-plan",
				Scope:    executionplans.Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: executionplans.Payload{
					SchemaVersion: 1,
					Features:      executionplans.FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
				},
				PlanHash: "hash-global",
			},
			{
				ID:       "provider-plan",
				Scope:    executionplans.Scope{Provider: "openai"},
				ScopeKey: "provider:openai",
				Version:  1,
				Active:   true,
				Name:     "openai",
				Payload: executionplans.Payload{
					SchemaVersion: 1,
					Features:      executionplans.FeatureFlags{Cache: false, Audit: true, Usage: true, Guardrails: false},
				},
				PlanHash: "hash-provider",
			},
		},
	}

	h := newExecutionPlanHandler(t, store, nil)
	e := echo.New()

	req := httptest.NewRequest(http.MethodPost, "/admin/api/v1/execution-plans/provider-plan/deactivate", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/admin/api/v1/execution-plans/:id/deactivate")
	c.SetPathValues(echo.PathValues{{Name: "id", Value: "provider-plan"}})

	if err := h.DeactivateExecutionPlan(c); err != nil {
		t.Fatalf("DeactivateExecutionPlan() error = %v", err)
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}

	views, err := h.plans.ListViews(context.Background())
	if err != nil {
		t.Fatalf("ListViews() error = %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("len(views) = %d, want 1", len(views))
	}
	if views[0].ID != "global-plan" {
		t.Fatalf("remaining view = %q, want global-plan", views[0].ID)
	}
}

func TestDeactivateExecutionPlanRejectsGlobalWorkflow(t *testing.T) {
	store := &executionPlanTestStore{
		versions: []executionplans.Version{
			{
				ID:       "global-plan",
				Scope:    executionplans.Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: executionplans.Payload{
					SchemaVersion: 1,
					Features:      executionplans.FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
				},
				PlanHash: "hash-global",
			},
		},
	}

	h := newExecutionPlanHandler(t, store, nil)
	e := echo.New()

	req := httptest.NewRequest(http.MethodPost, "/admin/api/v1/execution-plans/global-plan/deactivate", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/admin/api/v1/execution-plans/:id/deactivate")
	c.SetPathValues(echo.PathValues{{Name: "id", Value: "global-plan"}})

	if err := h.DeactivateExecutionPlan(c); err != nil {
		t.Fatalf("DeactivateExecutionPlan() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}

	body := decodeExecutionPlanErrorEnvelope(t, rec.Body.Bytes())
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

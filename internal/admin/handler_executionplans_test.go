package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
	"gomodel/internal/executionplans"
	"gomodel/internal/guardrails"
	"gomodel/internal/responsecache"
)

type executionPlanTestStore struct {
	versions []executionplans.Version
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
		scopeKey = "global"
	case input.Scope.Model == "":
		scopeKey = "provider:" + input.Scope.Provider
	default:
		scopeKey = "provider_model:" + input.Scope.Provider + ":" + input.Scope.Model
	}
	planHash := "hash-created"

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

func newExecutionPlanHandler(t *testing.T, store executionplans.Store, registry *guardrails.Registry) *Handler {
	t.Helper()

	service, err := executionplans.NewService(store, executionplans.NewCompiler(registry))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	return NewHandler(nil, nil, WithExecutionPlans(service), WithGuardrailsRegistry(registry))
}

func TestListExecutionPlans(t *testing.T) {
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
	if !body[0].EffectiveFeatures.Cache || !body[0].EffectiveFeatures.Audit || !body[0].EffectiveFeatures.Usage {
		t.Fatalf("effective features = %+v, want cache/audit/usage enabled", body[0].EffectiveFeatures)
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
	if body.Scope.Provider != "openai" || body.Scope.Model != "gpt-5" {
		t.Fatalf("scope = %#v, want openai/gpt-5", body.Scope)
	}
	if body.Name != "openai gpt-5" {
		t.Fatalf("name = %q, want openai gpt-5", body.Name)
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

	var body map[string]map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got := body["error"]["message"]; got != "unknown guardrail ref: missing-guardrail" {
		t.Fatalf("error message = %v, want unknown guardrail ref", got)
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

	var body map[string]map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got := body["error"]["type"]; got != "invalid_request_error" {
		t.Fatalf("error type = %v, want invalid_request_error", got)
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

	var body map[string]map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got := body["error"]["message"]; got != "cannot deactivate the global workflow" {
		t.Fatalf("error message = %v, want cannot deactivate the global workflow", got)
	}
}

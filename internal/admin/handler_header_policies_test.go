package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/headerpolicy"
	"github.com/enterpilot/gomodel/internal/workflows"
)

type headerPolicyTestStore struct {
	definitions map[string]headerpolicy.Definition
}

func (s *headerPolicyTestStore) List(context.Context) ([]headerpolicy.Definition, error) {
	result := make([]headerpolicy.Definition, 0, len(s.definitions))
	for _, definition := range s.definitions {
		result = append(result, definition)
	}
	return result, nil
}
func (s *headerPolicyTestStore) Get(_ context.Context, name string) (*headerpolicy.Definition, error) {
	definition, ok := s.definitions[name]
	if !ok {
		return nil, headerpolicy.ErrNotFound
	}
	return &definition, nil
}
func (s *headerPolicyTestStore) Upsert(_ context.Context, definition headerpolicy.Definition) error {
	s.definitions[definition.Name] = definition
	return nil
}
func (s *headerPolicyTestStore) UpsertMany(_ context.Context, definitions []headerpolicy.Definition) error {
	for _, definition := range definitions {
		s.definitions[definition.Name] = definition
	}
	return nil
}
func (s *headerPolicyTestStore) Delete(_ context.Context, name string) error {
	if _, ok := s.definitions[name]; !ok {
		return headerpolicy.ErrNotFound
	}
	delete(s.definitions, name)
	return nil
}
func (s *headerPolicyTestStore) Close() error { return nil }

func newHeaderPolicyHandler(t *testing.T) *Handler {
	t.Helper()
	service, err := headerpolicy.NewService(&headerPolicyTestStore{definitions: map[string]headerpolicy.Definition{}})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	return NewHandler(nil, nil, WithHeaderPolicyService(service))
}

func TestHeaderPolicyCRUDUsesFlattenedShape(t *testing.T) {
	h := newHeaderPolicyHandler(t)
	e := echo.New()
	req := httptest.NewRequest(http.MethodPut, "/admin/header-policies", bytes.NewBufferString(`{
		"name":"pin-beta",
		"methods":["post"],
		"paths":["/v1/*"],
		"when":[{"header":"X-Empty","equals":""}],
		"actions":[{"action":"set","header":"Anthropic-Beta","value":"context-1m"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	if err := h.UpsertHeaderPolicy(e.NewContext(req, rec)); err != nil {
		t.Fatalf("UpsertHeaderPolicy() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var saved headerpolicy.View
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if saved.Name != "pin-beta" || saved.Methods[0] != "POST" || saved.Paths[0] != "/v1/*" {
		t.Fatalf("saved = %#v", saved)
	}
	if saved.When[0].Equals == nil || *saved.When[0].Equals != "" {
		t.Fatalf("saved.When = %#v", saved.When)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/admin/header-policies", nil)
	listRec := httptest.NewRecorder()
	if err := h.ListHeaderPolicies(e.NewContext(listReq, listRec)); err != nil {
		t.Fatalf("ListHeaderPolicies() error = %v", err)
	}
	var listed []headerpolicy.View
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil || len(listed) != 1 {
		t.Fatalf("listed = %#v, err = %v", listed, err)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/admin/header-policies", bytes.NewBufferString(`{"name":"pin-beta"}`))
	deleteReq.Header.Set("Content-Type", "application/json")
	deleteRec := httptest.NewRecorder()
	if err := h.DeleteHeaderPolicy(e.NewContext(deleteReq, deleteRec)); err != nil {
		t.Fatalf("DeleteHeaderPolicy() error = %v", err)
	}
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", deleteRec.Code)
	}
}

func TestGuardrailEndpointRejectsHeaderModificationType(t *testing.T) {
	h := NewHandler(nil, nil, WithGuardrailService(newGuardrailService(t)))
	e := echo.New()
	req := httptest.NewRequest(http.MethodPut, "/admin/guardrails", bytes.NewBufferString(`{
		"name":"headers","type":"header_modification","config":{"actions":[{"action":"remove","header":"X-Debug"}]}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	if err := h.UpsertGuardrail(e.NewContext(req, rec)); err != nil {
		t.Fatalf("UpsertGuardrail() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHeaderPolicyMutationRollsBackWhenWorkflowRefreshFails(t *testing.T) {
	for _, operation := range []string{"upsert", "delete"} {
		t.Run(operation, func(t *testing.T) {
			policyStore := &headerPolicyTestStore{definitions: map[string]headerpolicy.Definition{
				"pin-beta": {
					Name:        "pin-beta",
					Description: "original",
					Actions:     []headerpolicy.Action{{Action: headerpolicy.ActionRemove, Header: "X-Debug"}},
				},
			}}
			policies, err := headerpolicy.NewService(policyStore)
			if err != nil {
				t.Fatalf("headerpolicy.NewService() error = %v", err)
			}
			if err := policies.Refresh(t.Context()); err != nil {
				t.Fatalf("policies.Refresh() error = %v", err)
			}

			workflowStore := &workflowTestStore{}
			workflowService, err := workflows.NewService(
				workflowStore,
				workflows.NewCompilerWithCatalogs(nil, policies, core.DefaultWorkflowFeatures()),
			)
			if err != nil {
				t.Fatalf("workflows.NewService() error = %v", err)
			}
			h := NewHandler(nil, nil, WithHeaderPolicyService(policies), WithWorkflows(workflowService))
			e := echo.New()

			var req *http.Request
			var call func(*echo.Context) error
			switch operation {
			case "upsert":
				workflowStore.failListActiveAt = 1
				workflowStore.listActiveErr = errors.New("workflow store unavailable")
				req = httptest.NewRequest(http.MethodPut, "/admin/header-policies", bytes.NewBufferString(`{
					"name":"pin-beta","description":"replacement",
					"actions":[{"action":"remove","header":"X-Trace"}]
				}`))
				call = h.UpsertHeaderPolicy
			case "delete":
				// Delete checks active references before refreshing the runtime snapshot.
				workflowStore.failListActiveAt = 2
				workflowStore.listActiveErr = errors.New("workflow store unavailable")
				req = httptest.NewRequest(http.MethodDelete, "/admin/header-policies", bytes.NewBufferString(`{"name":"pin-beta"}`))
				call = h.DeleteHeaderPolicy
			}
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			if err := call(e.NewContext(req, rec)); err != nil {
				t.Fatalf("handler error = %v", err)
			}
			if rec.Code < http.StatusInternalServerError {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}

			stored, ok := policies.Get("pin-beta")
			if !ok || stored.Description != "original" || len(stored.Actions) != 1 || stored.Actions[0].Header != "X-Debug" {
				t.Fatalf("catalog was not rolled back: %#v, exists = %v", stored, ok)
			}
			persisted, ok := policyStore.definitions["pin-beta"]
			if !ok || persisted.Description != "original" || len(persisted.Actions) != 1 || persisted.Actions[0].Header != "X-Debug" {
				t.Fatalf("store was not rolled back: %#v, exists = %v", persisted, ok)
			}
		})
	}
}

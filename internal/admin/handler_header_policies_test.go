package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/headerpolicy"
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

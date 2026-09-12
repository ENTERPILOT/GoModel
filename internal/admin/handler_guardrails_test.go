package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/internal/plugins/builtin"
	"github.com/enterpilot/gomodel/internal/plugins/builtin/llmaltering"
	"github.com/enterpilot/gomodel/internal/workflows"
)

type guardrailTestStore struct {
	definitions map[string]guardrails.Definition
}

func newGuardrailTestStore(definitions ...guardrails.Definition) *guardrailTestStore {
	store := &guardrailTestStore{definitions: make(map[string]guardrails.Definition, len(definitions))}
	for _, definition := range definitions {
		store.definitions[definition.Name] = definition
	}
	return store
}

func (s *guardrailTestStore) List(context.Context) ([]guardrails.Definition, error) {
	result := make([]guardrails.Definition, 0, len(s.definitions))
	for _, definition := range s.definitions {
		result = append(result, definition)
	}
	return result, nil
}

func (s *guardrailTestStore) Get(_ context.Context, name string) (*guardrails.Definition, error) {
	definition, ok := s.definitions[name]
	if !ok {
		return nil, guardrails.ErrNotFound
	}
	copy := definition
	return &copy, nil
}

func (s *guardrailTestStore) Upsert(_ context.Context, definition guardrails.Definition) error {
	s.definitions[definition.Name] = definition
	return nil
}

func (s *guardrailTestStore) UpsertMany(_ context.Context, definitions []guardrails.Definition) error {
	for _, definition := range definitions {
		s.definitions[definition.Name] = definition
	}
	return nil
}

func (s *guardrailTestStore) Delete(_ context.Context, name string) error {
	if _, ok := s.definitions[name]; !ok {
		return guardrails.ErrNotFound
	}
	delete(s.definitions, name)
	return nil
}

func (s *guardrailTestStore) Close() error { return nil }

func rawGuardrailConfig(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)

	return raw
}

func newGuardrailService(t *testing.T, definitions ...guardrails.Definition) *guardrails.Service {
	t.Helper()

	catalog := plugins.NewCatalog()
	for _, factory := range builtin.All() {
		err := catalog.Register(factory, plugins.SourceBuiltin)
		require.NoError(t, err)
	}
	service, err := guardrails.NewService(newGuardrailTestStore(definitions...), catalog, plugins.HostDeps{})
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	return service
}

func newGuardrailHandler(t *testing.T, definitions ...guardrails.Definition) *Handler {
	t.Helper()
	return NewHandler(nil, nil, WithGuardrailService(newGuardrailService(t, definitions...)))
}

func TestListGuardrails(t *testing.T) {
	h := newGuardrailHandler(t, guardrails.Definition{
		Name: "policy-system",
		Type: "system_prompt",
		Config: rawGuardrailConfig(t, map[string]any{
			"mode":    "inject",
			"content": "be precise",
		}),
	})

	c, rec := echotest.Get(t, "/admin/guardrails")
	err := h.ListGuardrails(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	body := echotest.Decode[[]guardrails.View](t, rec)
	require.Len(t, body, 1)
	assert.Equal(t, "policy-system", body[0].Name)
	assert.NotEmpty(t, body[0].Summary)
}

func TestListGuardrailTypes(t *testing.T) {
	h := newGuardrailHandler(t)
	c, rec := echotest.Get(t, "/admin/guardrails/types")
	err := h.ListGuardrailTypes(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	body := echotest.Decode[[]guardrails.TypeDefinition](t, rec)

	byType := map[string]guardrails.TypeDefinition{}
	for _, typeDef := range body {
		byType[typeDef.Type] = typeDef
	}
	assert.Contains(t, byType, "system_prompt")
	typeDef, ok := byType["llm_based_altering"]
	require.True(t, ok, "body = %#v, want an llm_based_altering type definition", body)
	require.NotEmpty(t, typeDef.Defaults)

	var defaults map[string]any
	err = json.Unmarshal(typeDef.Defaults, &defaults)
	require.NoError(t, err)

	prompt, ok := defaults["prompt"].(string)
	require.True(t, ok, "llm_based_altering defaults.prompt = %#v, want string", defaults["prompt"])
	assert.NotEmpty(t, strings.TrimSpace(prompt))
	assert.Len(t, typeDef.Phases, 2)
	assert.Equal(t, "builtin", typeDef.Source)
	assert.True(t, typeDef.Mutates)
	assert.True(t, typeDef.Guardrail)

	for _, field := range typeDef.Fields {
		if field.Key == "max_tokens" {
			assert.Equal(t, fmt.Sprint(defaults["max_tokens"]), fmt.Sprint(field.Default), "max_tokens field default disagrees with defaults")
		}
	}
}

func TestUpsertGuardrail(t *testing.T) {
	h := newGuardrailHandler(t)

	c, rec := echotest.Request(t, http.MethodPut, "/admin/guardrails", `{
		"name":"policy-system",
		"type":"system_prompt",
		"description":"Default policy",
		"user_path":"team/alpha",
		"config":{"mode":"override","content":"Respond carefully."}
	}`)
	err := h.UpsertGuardrail(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	guardrail, ok := h.guardrailDefs.Get("policy-system")
	require.True(t, ok)
	require.NotNil(t, guardrail)
	assert.Equal(t, "system_prompt", guardrail.Type)
	assert.Equal(t, "/team/alpha", guardrail.UserPath)
}

func TestUpsertGuardrailLLMBasedAltering(t *testing.T) {
	h := newGuardrailHandler(t)

	c, rec := echotest.Request(t, http.MethodPut, "/admin/guardrails", `{
		"name":"privacy",
		"type":"llm_based_altering",
		"description":"Rewrite user PII",
		"config":{"model":"gpt-4o-mini","roles":["user","tool"]}
	}`)
	err := h.UpsertGuardrail(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	guardrail, ok := h.guardrailDefs.Get("privacy")
	require.True(t, ok)
	require.NotNil(t, guardrail)
	assert.Equal(t, "llm_based_altering", guardrail.Type)

	var cfg map[string]any
	err = json.Unmarshal(guardrail.Config, &cfg)
	require.NoError(t, err)
	assert.Equal(t, "gpt-4o-mini", cfg["model"])
	assert.InDelta(t, float64(llmaltering.DefaultMaxTokens), cfg["max_tokens"], 0, "max_tokens default must be filled in")
}

func TestUpsertGuardrailLLMBasedAlteringNormalizesProviderHintIntoModel(t *testing.T) {
	h := newGuardrailHandler(t)

	c, rec := echotest.Request(t, http.MethodPut, "/admin/guardrails", `{
		"name":"privacy",
		"type":"llm_based_altering",
		"description":"Rewrite user PII",
		"config":{"model":"gpt-4o-mini","provider":"openai","roles":["user"]}
	}`)
	err := h.UpsertGuardrail(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	guardrail, ok := h.guardrailDefs.Get("privacy")
	require.True(t, ok)
	require.NotNil(t, guardrail)

	var cfg map[string]any
	err = json.Unmarshal(guardrail.Config, &cfg)
	require.NoError(t, err)
	assert.Equal(t, "openai/gpt-4o-mini", cfg["model"])
	assert.NotContains(t, cfg, "provider", "provider hint must be folded into model")
}

func TestUpsertGuardrailRejectsSlashInName(t *testing.T) {
	h := newGuardrailHandler(t)

	c, rec := echotest.Request(t, http.MethodPut, "/admin/guardrails", `{
		"name":"privacy/redactor",
		"type":"llm_based_altering",
		"config":{"model":"gpt-4o-mini","roles":["user"]}
	}`)
	err := h.UpsertGuardrail(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	envelope := echotest.Decode[workflowErrorEnvelope](t, rec)
	assert.Equal(t, string(core.ErrorTypeInvalidRequest), envelope.Error.Type)
	assert.Equal(t, "guardrail name cannot contain '/'", envelope.Error.Message)
	assert.Nil(t, envelope.Error.Param)
	assert.Nil(t, envelope.Error.Code)
}

func TestDeleteGuardrailRejectsActiveWorkflowReference(t *testing.T) {
	guardrailService := newGuardrailService(t, guardrails.Definition{
		Name: "policy-system",
		Type: "system_prompt",
		Config: rawGuardrailConfig(t, map[string]any{
			"mode":    "inject",
			"content": "be precise",
		}),
	})
	planStore := &workflowTestStore{
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
					Guardrails:    []workflows.GuardrailStep{{Ref: "policy-system", Step: 10}},
				},
				WorkflowHash: "hash-global",
			},
		},
	}
	planService, err := workflows.NewService(planStore, workflows.NewCompilerWithFeatureCaps(guardrailService, core.DefaultWorkflowFeatures()))
	require.NoError(t, err)
	err = planService.Refresh(context.Background())
	require.NoError(t, err)

	h := NewHandler(nil, nil, WithGuardrailService(guardrailService), WithWorkflows(planService))
	c, rec := echotest.Request(t, http.MethodDelete, "/admin/guardrails", `{"name":"policy-system"}`)
	err = h.DeleteGuardrail(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	envelope := echotest.Decode[workflowErrorEnvelope](t, rec)
	assert.Equal(t, "guardrail is used by active workflows: global", envelope.Error.Message)
}

func TestDeleteGuardrailIgnoresDisabledWorkflowGuardrailRefs(t *testing.T) {
	guardrailService := newGuardrailService(t, guardrails.Definition{
		Name: "policy-system",
		Type: "system_prompt",
		Config: rawGuardrailConfig(t, map[string]any{
			"mode":    "inject",
			"content": "be precise",
		}),
	})
	planStore := &workflowTestStore{
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
					Guardrails:    []workflows.GuardrailStep{{Ref: "policy-system", Step: 10}},
				},
				WorkflowHash: "hash-global",
			},
		},
	}
	planService, err := workflows.NewService(planStore, workflows.NewCompilerWithFeatureCaps(guardrailService, core.DefaultWorkflowFeatures()))
	require.NoError(t, err)
	err = planService.Refresh(context.Background())
	require.NoError(t, err)

	h := NewHandler(nil, nil, WithGuardrailService(guardrailService), WithWorkflows(planService))
	c, rec := echotest.Request(t, http.MethodDelete, "/admin/guardrails", `{"name":"policy-system"}`)
	err = h.DeleteGuardrail(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, rec.Code)
	_, ok := h.guardrailDefs.Get("policy-system")
	assert.False(t, ok)
}

// Retyping a guardrail must not strand an active workflow on a phase the
// new type does not implement: the definition would be stored and the
// recompile would fail.
func TestUpsertGuardrailRejectsRetypeUsedInUnsupportedPhase(t *testing.T) {
	guardrailService := newGuardrailService(t, guardrails.Definition{
		Name:   "redact",
		Type:   "string_replace",
		Config: rawGuardrailConfig(t, map[string]any{"rules": "ACME => [co]"}),
	})
	planStore := &workflowTestStore{
		versions: []workflows.Version{
			{
				ID:       "global-workflow",
				Scope:    workflows.Scope{},
				ScopeKey: "global",
				Version:  1,
				Active:   true,
				Name:     "global",
				Payload: workflows.Payload{
					SchemaVersion: 2,
					Features:      workflows.FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: true},
					Steps:         []workflows.Step{{Ref: "redact", Phase: "response", Step: 10}},
				},
				WorkflowHash: "hash-global",
			},
		},
	}
	planService, err := workflows.NewService(planStore, workflows.NewCompilerWithFeatureCaps(guardrailService, core.DefaultWorkflowFeatures()))
	require.NoError(t, err)
	err = planService.Refresh(context.Background())
	require.NoError(t, err)

	h := NewHandler(nil, nil, WithGuardrailService(guardrailService), WithWorkflows(planService))

	upsert := func(body string) *httptest.ResponseRecorder {
		c, rec := echotest.Request(t, http.MethodPut, "/admin/guardrails", body)
		require.NoError(t, h.UpsertGuardrail(c))
		return rec
	}

	rec := upsert(`{"name":"redact","type":"system_prompt","config":{"mode":"inject","content":"be precise"}}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	envelope := echotest.Decode[workflowErrorEnvelope](t, rec)
	assert.Equal(t, "guardrail type system_prompt does not support the phases used by active workflows: global (response)", envelope.Error.Message)
	got, _ := guardrailService.Get("redact")
	require.NotNil(t, got)
	assert.Equal(t, "string_replace", got.Type, "rejected retype must leave the definition untouched")

	// The same type with a new config keeps the phase and is accepted.
	rec = upsert(`{"name":"redact","type":"string_replace","config":{"rules":"ACME => [x]"}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

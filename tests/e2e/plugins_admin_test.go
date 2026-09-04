//go:build e2e

package e2e

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlugins_AdminSurface_E2E(t *testing.T) {
	fx := setupPluginServer(t)

	t.Run("plugins lists the builtins with kinds", func(t *testing.T) {
		var listed []struct {
			Name    string   `json:"name"`
			Kinds   []string `json:"kinds"`
			Source  string   `json:"source"`
			Health  string   `json:"health"`
			Mutates bool     `json:"mutates"`
		}
		fx.adminJSON(t, http.MethodGet, adminPluginsPath, nil, http.StatusOK, &listed)

		byName := map[string][]string{}
		for _, p := range listed {
			byName[p.Name] = p.Kinds
			assert.Equal(t, "builtin", p.Source, p.Name)
			assert.Equal(t, "ok", p.Health, p.Name)
			assert.NotEmpty(t, p.Kinds, "%s should list its hook kinds", p.Name)
		}
		for _, name := range []string{"system_prompt", "llm_based_altering", "string_replace", "header_edit", "llm_judge"} {
			assert.Contains(t, byName, name)
		}
		assert.ElementsMatch(t, []string{"prompt", "response", "stream"}, byName["string_replace"])
		assert.ElementsMatch(t, []string{"prompt", "response"}, byName["header_edit"])
		assert.ElementsMatch(t, []string{"prompt", "response", "stream"}, byName["llm_judge"])
	})

	t.Run("guardrail types expose phases", func(t *testing.T) {
		var types []struct {
			Type   string   `json:"type"`
			Phases []string `json:"phases"`
			Source string   `json:"source"`
			Fields []struct {
				Key string `json:"key"`
			} `json:"fields"`
		}
		fx.adminJSON(t, http.MethodGet, adminGuardrailTypesPath, nil, http.StatusOK, &types)

		phases := map[string][]string{}
		fields := map[string]int{}
		for _, typ := range types {
			phases[typ.Type] = typ.Phases
			fields[typ.Type] = len(typ.Fields)
			assert.Equal(t, "builtin", typ.Source, typ.Type)
		}
		for _, name := range []string{"string_replace", "header_edit", "llm_judge"} {
			assert.Positive(t, fields[name], "%s should expose its config schema", name)
		}
		assert.ElementsMatch(t, []string{"prompt", "response", "stream"}, phases["string_replace"])
		assert.ElementsMatch(t, []string{"prompt", "response"}, phases["header_edit"])
		assert.ElementsMatch(t, []string{"prompt", "response", "stream"}, phases["llm_judge"])
	})

	t.Run("instances, workflow guardrails and v2 workflow", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })

		var view struct {
			Name     string   `json:"name"`
			Type     string   `json:"type"`
			FailMode string   `json:"fail_mode"`
			Timeout  int      `json:"timeout_ms"`
			Phases   []string `json:"phases"`
		}
		fx.adminJSON(t, http.MethodPut, adminGuardrailsPath,
			guardrailDef("redact", "string_replace", map[string]any{"rules": "secret => [redacted]"}, nil),
			http.StatusOK, &view)
		assert.Equal(t, "string_replace", view.Type)
		assert.Empty(t, view.FailMode, "fail_mode defaults to empty (closed)")
		assert.ElementsMatch(t, []string{"prompt", "response", "stream"}, view.Phases)

		fx.adminJSON(t, http.MethodPut, adminGuardrailsPath,
			guardrailDef("judge", "llm_judge", map[string]any{"model": "gpt-4"}, map[string]any{"fail_mode": "open", "timeout_ms": 5000}),
			http.StatusOK, &view)
		assert.Equal(t, "open", view.FailMode)
		assert.Equal(t, 5000, view.Timeout)

		var available []struct {
			Name   string   `json:"name"`
			Type   string   `json:"type"`
			Phases []string `json:"phases"`
		}
		fx.adminJSON(t, http.MethodGet, adminWorkflowGuardrailsPath, nil, http.StatusOK, &available)
		require.Len(t, available, 2)
		for _, item := range available {
			assert.NotEmpty(t, item.Type, item.Name)
			assert.NotEmpty(t, item.Phases, "%s should list phases", item.Name)
		}

		fx.activate(t,
			workflowStep{Ref: "redact", Phase: "prompt", Step: 10},
			workflowStep{Ref: "redact", Phase: "response", Step: 10},
			workflowStep{Ref: "redact", Phase: "stream", Step: 10},
			workflowStep{Ref: "judge", Phase: "prompt", Step: 20},
		)

		var workflowsList []struct {
			Name    string `json:"name"`
			Active  bool   `json:"active"`
			Payload struct {
				SchemaVersion int            `json:"schema_version"`
				Steps         []workflowStep `json:"steps"`
			} `json:"workflow_payload"`
			ChainHashes map[string]string `json:"chain_hashes"`
		}
		fx.adminJSON(t, http.MethodGet, adminWorkflowsPath, nil, http.StatusOK, &workflowsList)
		require.Len(t, workflowsList, 1)
		active := workflowsList[0]
		assert.Equal(t, pluginsWorkflowName, active.Name)
		assert.True(t, active.Active)
		assert.Equal(t, 2, active.Payload.SchemaVersion)
		assert.Len(t, active.Payload.Steps, 4)
		for _, phase := range []string{"prompt", "response", "stream"} {
			assert.NotEmpty(t, active.ChainHashes[phase], "chain hash for %s phase", phase)
		}

		// An instance referenced by the active workflow cannot be deleted.
		status, body := fx.admin(t, http.MethodDelete, adminGuardrailsPath, map[string]string{"name": "redact"})
		assert.Equal(t, http.StatusBadRequest, status, string(body))
	})

	t.Run("workflow step referencing an unknown phase is rejected", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		fx.mustPutGuardrail(t, guardrailDef("headers", "header_edit", map[string]any{"response_set": "X-Policy: checked"}, nil))

		payload := map[string]any{
			"name": pluginsWorkflowName,
			"workflow_payload": map[string]any{
				"schema_version": 2,
				"features":       map[string]any{"guardrails": true},
				"steps":          []workflowStep{{Ref: "headers", Phase: "stream", Step: 1}},
			},
		}
		status, body := fx.admin(t, http.MethodPost, adminWorkflowsPath, payload)
		assert.Equal(t, http.StatusBadRequest, status, "header_edit has no stream hook: %s", string(body))
	})
}

func TestPlugins_HeaderEdit_E2E(t *testing.T) {
	fx := setupPluginServer(t)

	t.Run("response_set adds a header to the client response", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		fx.mustPutGuardrail(t, guardrailDef("headers", "header_edit", map[string]any{"response_set": "X-Policy: checked"}, nil))
		fx.activate(t, workflowStep{Ref: "headers", Phase: "prompt", Step: 1})

		resp := fx.chat(t, "hello", false)
		assert.Equal(t, "checked", resp.Header.Get("X-Policy"))
		_, text := readChat(t, resp)
		assert.Equal(t, "Mock response to: hello", text)
	})

	t.Run("request_set of Authorization is rejected", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		status, body := fx.putGuardrail(t, guardrailDef("auth", "header_edit", map[string]any{"request_set": "Authorization: Bearer leaked"}, nil))
		assert.Equal(t, http.StatusBadRequest, status, string(body))
		assert.Contains(t, string(body), "Authorization")

		var defs []map[string]any
		fx.adminJSON(t, http.MethodGet, adminGuardrailsPath, nil, http.StatusOK, &defs)
		assert.Empty(t, defs, "rejected instance must not be stored")
	})
}

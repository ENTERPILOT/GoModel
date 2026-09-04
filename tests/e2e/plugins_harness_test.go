//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/admin"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/pluginload"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/internal/plugins/builtin"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/workflows"
)

// Admin endpoints exercised by the plugin tests.
const (
	adminPluginsPath            = "/admin/plugins"
	adminGuardrailsPath         = "/admin/guardrails"
	adminGuardrailTypesPath     = "/admin/guardrails/types"
	adminWorkflowsPath          = "/admin/workflows"
	adminWorkflowGuardrailsPath = "/admin/workflows/guardrails"
	messagesPath                = "/v1/messages"

	guardrailHeader = "X-GoModel-Guardrail"
	userPathHeader  = "X-GoModel-User-Path"

	pluginsWorkflowName = "e2e-plugins"
)

// pluginFixture is a gateway wired like the app for plugins: a SQLite
// backed guardrail store and workflow store, the built-in plugin catalog
// (plus any shared objects), and the admin API to drive them.
type pluginFixture struct {
	url string
}

// workflowStep is one v2 workflow step as POST /admin/workflows takes it.
type workflowStep struct {
	Ref   string `json:"ref"`
	Phase string `json:"phase"`
	Step  int    `json:"step"`
}

// errorEnvelope decodes the OpenAI error envelope with its optional code.
type errorEnvelope struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
}

// setupPluginServer starts a gateway whose guardrails are built from the
// built-in plugins plus loaded shared objects. Internal inference (llm_judge)
// is routed through the same registry, so it reaches the shared mock.
func setupPluginServer(t *testing.T, loaded ...pluginload.Loaded) *pluginFixture {
	t.Helper()
	ctx := context.Background()

	store, err := storage.NewSQLite(storage.SQLiteConfig{Path: filepath.Join(t.TempDir(), "plugins.db")})
	require.NoError(t, err, "open sqlite storage")
	t.Cleanup(func() { _ = store.Close() })

	catalog := plugins.NewCatalog()
	for _, factory := range builtin.All() {
		require.NoError(t, catalog.Register(factory, plugins.SourceBuiltin))
	}
	for _, l := range loaded {
		require.NoError(t, catalog.Register(l.Factory, plugins.Source(l.Path), plugins.RegisterOptions{SingleInstance: l.SingleInstance}))
	}

	registry := setupE2ERegistry(t, "")
	router, err := providers.NewRouter(registry)
	require.NoError(t, err, "create router")

	guardrailResult, err := guardrails.New(ctx, store, time.Hour, catalog, plugins.HostDeps{Chat: router})
	require.NoError(t, err, "init guardrails")
	t.Cleanup(func() { _ = guardrailResult.Close() })

	compiler := workflows.NewCompilerWithFeatureCaps(guardrailResult.Service, core.WorkflowFeatures{Guardrails: true})
	workflowResult, err := workflows.New(ctx, store, compiler, time.Hour)
	require.NoError(t, err, "init workflows")
	t.Cleanup(func() { _ = workflowResult.Close() })
	// The admin API refreshes workflows after every guardrail change and
	// requires the managed default global workflow the app seeds at startup.
	require.NoError(t, workflowResult.Service.EnsureDefaultGlobal(ctx, workflows.CreateInput{
		Activate:    true,
		Name:        workflows.ManagedDefaultGlobalName,
		Description: workflows.ManagedDefaultGlobalDescription,
		Payload:     workflows.Payload{SchemaVersion: 2},
	}), "seed default workflow")
	require.NoError(t, workflowResult.Service.Refresh(ctx), "load workflows")

	ts := httptest.NewServer(setupE2EServer(t, e2eServerOptions{
		registry:              registry,
		adminEndpointsEnabled: true,
		adminOptions: []admin.Option{
			admin.WithWorkflows(workflowResult.Service),
			admin.WithGuardrailService(guardrailResult.Service),
			admin.WithPluginCatalog(catalog),
		},
		workflowPolicyResolver:   workflowResult.Service,
		translatedRequestPatcher: guardrails.NewWorkflowRequestPatcher(workflowResult.Service),
		pluginChains:             workflowResult.Service,
	}))
	t.Cleanup(ts.Close)
	t.Cleanup(resetMock)

	return &pluginFixture{url: ts.URL}
}

// resetMock removes any scripted behaviour from the shared mock provider.
func resetMock() {
	mockServer.SetCustomHandler(nil)
	mockServer.SetResponseDelay(0)
	mockServer.ResetRequests()
}

// do sends a JSON request with optional headers and returns the response.
func (f *pluginFixture) do(t *testing.T, method, path string, payload any, headers map[string]string) *http.Response {
	t.Helper()
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		require.NoError(t, err)
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, f.url+path, body)
	require.NoError(t, err)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// admin sends an admin request and returns the status and body.
func (f *pluginFixture) admin(t *testing.T, method, path string, payload any) (int, []byte) {
	t.Helper()
	resp := f.do(t, method, path, payload, nil)
	defer closeBody(resp)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, body
}

// adminJSON sends an admin request expecting the given status and decodes
// the JSON body into out.
func (f *pluginFixture) adminJSON(t *testing.T, method, path string, payload any, wantStatus int, out any) {
	t.Helper()
	status, body := f.admin(t, method, path, payload)
	require.Equal(t, wantStatus, status, "%s %s: %s", method, path, string(body))
	if out != nil {
		require.NoError(t, json.Unmarshal(body, out), "%s %s: %s", method, path, string(body))
	}
}

// guardrailDef builds a PUT /admin/guardrails payload. extra holds top-level
// fields such as fail_mode or timeout_ms.
func guardrailDef(name, typ string, config map[string]any, extra map[string]any) map[string]any {
	def := map[string]any{"name": name, "type": typ, "config": config}
	maps.Copy(def, extra)
	return def
}

// putGuardrail creates or updates an instance and returns the status and body.
func (f *pluginFixture) putGuardrail(t *testing.T, def map[string]any) (int, []byte) {
	t.Helper()
	return f.admin(t, http.MethodPut, adminGuardrailsPath, def)
}

// mustPutGuardrail creates an instance and fails the test on any error.
func (f *pluginFixture) mustPutGuardrail(t *testing.T, def map[string]any) {
	t.Helper()
	status, body := f.putGuardrail(t, def)
	require.Equal(t, http.StatusOK, status, "PUT %s %v: %s", adminGuardrailsPath, def["name"], string(body))
}

// activate publishes a new active global workflow (schema version 2) with
// the given steps. The previous global version is superseded.
func (f *pluginFixture) activate(t *testing.T, steps ...workflowStep) {
	t.Helper()
	f.activateScoped(t, "", steps...)
}

// activateScoped publishes an active workflow for one user path (global when
// empty) and returns the version id.
func (f *pluginFixture) activateScoped(t *testing.T, userPath string, steps ...workflowStep) string {
	t.Helper()
	if steps == nil {
		steps = []workflowStep{}
	}
	payload := map[string]any{
		"name":            pluginsWorkflowName,
		"scope_user_path": userPath,
		"workflow_payload": map[string]any{
			"schema_version": 2,
			"features":       map[string]any{"guardrails": len(steps) > 0},
			"steps":          steps,
		},
	}
	var version struct {
		ID string `json:"id"`
	}
	f.adminJSON(t, http.MethodPost, adminWorkflowsPath, payload, http.StatusCreated, &version)
	require.NotEmpty(t, version.ID)
	return version.ID
}

// reset returns the fixture to a clean state: scoped workflows are
// deactivated, the global workflow is replaced by one without steps, every
// instance is deleted, and the mock is unscripted.
func (f *pluginFixture) reset(t *testing.T) {
	t.Helper()
	var views []struct {
		ID    string `json:"id"`
		Scope struct {
			UserPath string `json:"scope_user_path"`
		} `json:"scope"`
	}
	f.adminJSON(t, http.MethodGet, adminWorkflowsPath, nil, http.StatusOK, &views)
	for _, view := range views {
		if view.Scope.UserPath == "" {
			continue
		}
		status, body := f.admin(t, http.MethodPost, adminWorkflowsPath+"/"+view.ID+"/deactivate", nil)
		require.Equal(t, http.StatusNoContent, status, "deactivate %s: %s", view.ID, string(body))
	}
	f.activate(t)

	var defs []struct {
		Name string `json:"name"`
	}
	f.adminJSON(t, http.MethodGet, adminGuardrailsPath, nil, http.StatusOK, &defs)
	for _, def := range defs {
		status, body := f.admin(t, http.MethodDelete, adminGuardrailsPath, map[string]string{"name": def.Name})
		require.Equal(t, http.StatusNoContent, status, "delete guardrail %s: %s", def.Name, string(body))
	}
	resetMock()
}

// chat sends a chat completion for one user message.
func (f *pluginFixture) chat(t *testing.T, msg string, stream bool) *http.Response {
	t.Helper()
	req := defaultChatReq(msg)
	req.Stream = stream
	return f.do(t, http.MethodPost, chatCompletionsPath, req, nil)
}

// readChat decodes a 200 chat completion and returns the first choice's text.
func readChat(t *testing.T, resp *http.Response) (core.ChatResponse, string) {
	t.Helper()
	defer closeBody(resp)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var chat core.ChatResponse
	require.NoError(t, json.Unmarshal(body, &chat), string(body))
	require.NotEmpty(t, chat.Choices)
	return chat, core.ExtractTextContent(chat.Choices[0].Message.Content)
}

// readError decodes an OpenAI error envelope and checks the status.
func readError(t *testing.T, resp *http.Response, wantStatus int) errorEnvelope {
	t.Helper()
	defer closeBody(resp)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, wantStatus, resp.StatusCode, string(body))
	var envelope errorEnvelope
	require.NoError(t, json.Unmarshal(body, &envelope), string(body))
	return envelope
}

// readBody drains a response body as a string.
func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer closeBody(resp)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body)
}

// lastUpstreamChat returns the most recent chat request the mock received.
func lastUpstreamChat(t *testing.T) core.ChatRequest {
	t.Helper()
	requests := mockServer.Requests()
	require.NotEmpty(t, requests, "mock received no requests")
	last := requests[len(requests)-1]
	require.Equal(t, "/chat/completions", last.Path)
	var upstream core.ChatRequest
	require.NoError(t, json.Unmarshal(last.Body, &upstream))
	return upstream
}

// scriptMockChat makes the shared mock answer chat completions with a fixed
// text: non-streaming requests get content, streaming ones get chunks as
// separate SSE deltas (so a match can straddle chunk boundaries). Other
// paths fall through to the built-in mock behaviour.
func scriptMockChat(t *testing.T, content string, chunks []string) {
	t.Helper()
	mockServer.SetCustomHandler(func(w http.ResponseWriter, r *http.Request) bool {
		req, ok := decodeMockChat(r)
		if !ok {
			return false
		}
		if req.Stream {
			writeMockChatStream(w, req.Model, chunks)
		} else {
			writeMockChatCompletion(w, req.Model, content)
		}
		return true
	})
	t.Cleanup(resetMock)
}

// scriptMockJudge answers llm_judge calls (recognised by the <CONTENT>
// marker in the user message) with reply, or with an upstream error when
// status is not 200. Ordinary requests keep the built-in echo behaviour.
func scriptMockJudge(t *testing.T, reply string, status int) {
	t.Helper()
	mockServer.SetCustomHandler(func(w http.ResponseWriter, r *http.Request) bool {
		req, ok := decodeMockChat(r)
		if !ok || !isJudgeRequest(req) {
			return false
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = fmt.Fprintf(w, `{"error": {"message": "judge unavailable", "type": "api_error"}}`)
			return true
		}
		writeMockChatCompletion(w, req.Model, reply)
		return true
	})
	t.Cleanup(resetMock)
}

func isJudgeRequest(req core.ChatRequest) bool {
	for _, m := range req.Messages {
		if strings.Contains(core.ExtractTextContent(m.Content), "<CONTENT>") {
			return true
		}
	}
	return false
}

func decodeMockChat(r *http.Request) (core.ChatRequest, bool) {
	var req core.ChatRequest
	if r.URL.Path != "/chat/completions" {
		return req, false
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return req, false
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return req, false
	}
	return req, true
}

func writeMockChatCompletion(w http.ResponseWriter, model, content string) {
	response := core.ChatResponse{
		ID:      "chatcmpl-scripted",
		Object:  "chat.completion",
		Model:   model,
		Created: time.Now().Unix(),
		Choices: []core.Choice{{
			Index:        0,
			Message:      core.ResponseMessage{Role: "assistant", Content: content},
			FinishReason: "stop",
		}},
		Usage: core.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func writeMockChatStream(w http.ResponseWriter, model string, chunks []string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}
	for i, chunk := range chunks {
		delta := map[string]any{
			"id":      "chatcmpl-scripted-stream",
			"object":  "chat.completion.chunk",
			"model":   model,
			"created": time.Now().Unix(),
			"choices": []map[string]any{{
				"index":         0,
				"delta":         map[string]any{"content": chunk},
				"finish_reason": nil,
			}},
		}
		if i == len(chunks)-1 {
			delta["choices"].([]map[string]any)[0]["finish_reason"] = "stop"
		}
		data, _ := json.Marshal(delta)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		flush()
		time.Sleep(5 * time.Millisecond)
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flush()
}

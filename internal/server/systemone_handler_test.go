package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/usage"
)

const (
	systemOneQuestions = `{"refund":{"type":"noul","instructions":"Is the customer asking for money back?"}}`
	systemOneAnswer    = `{"model":"kev-1.0","answers":{"refund":{"type":"noul","noul":0.98}},"usage":{"input_tokens":275,"output_tokens":20}}`
)

func systemOneBody(model string) string {
	return `{"model":"` + model + `","state":"I was charged twice for card 4111.","questions":` + systemOneQuestions + `}`
}

// systemOneAliasResolver maps virtual model names to concrete selectors.
type systemOneAliasResolver map[string]core.ModelSelector

func (r systemOneAliasResolver) ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	if selector, ok := r[requested.RequestedQualifiedModel()]; ok {
		return selector, true, nil
	}
	selector, err := requested.Normalize()
	return selector, false, err
}

// newSystemOneProvider configures a local Kev server (type jev, named kev),
// OpenRouter, and an OpenAI chat model, answering every passthrough with body.
func newSystemOneProvider(body string) *mockProvider {
	return &mockProvider{
		supportedModels: []string{"kev-latest", "typesafe/jev-1.13", "gpt-5-mini"},
		providerTypes: map[string]string{
			"kev/kev-latest":               "jev",
			"openrouter/typesafe/jev-1.13": "openrouter",
			"openai/gpt-5-mini":            "openai",
		},
		providerNames: map[string]string{
			"kev/kev-latest":               "kev",
			"openrouter/typesafe/jev-1.13": "openrouter",
			"openai/gpt-5-mini":            "openai",
		},
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers:    map[string][]string{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		},
	}
}

var systemOneAliases = systemOneAliasResolver{
	"decider": {Provider: "kev", Model: "kev-latest"},
	"chatty":  {Provider: "openai", Model: "gpt-5-mini"},
}

func forwardedSystemOneBody(t *testing.T, provider *mockProvider) map[string]any {
	t.Helper()
	require.NotNil(t, provider.lastPassthroughReq)
	raw, err := io.ReadAll(provider.lastPassthroughReq.Body)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body))

	return body
}

// Without a jev provider the endpoint does not exist, whatever the model.
func TestSystemOne_UnavailableWithoutJevProvider(t *testing.T) {
	provider := &mockProvider{
		supportedModels: []string{"gpt-5-mini"},
		providerTypes:   map[string]string{"openai/gpt-5-mini": "openai"},
		providerNames:   map[string]string{"openai/gpt-5-mini": "openai"},
	}
	handler := NewHandler(provider, nil, nil, nil)

	c, rec := echotest.Post(t, "/v1/systemone", systemOneBody("gpt-5-mini"))
	require.NoError(t, handler.SystemOne(c))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "jev provider")
	assert.Nil(t, provider.lastPassthroughReq)
}

// A virtual model resolves to its System One target, and the body reaches the
// provider unchanged except for the concrete model name.
func TestSystemOne_ForwardsNativelyAndRecordsUsage(t *testing.T) {
	provider := newSystemOneProvider(systemOneAnswer)
	usageLogger := &collectingUsageLogger{config: usage.Config{Enabled: true}}
	handler := newHandlerWithAuthorizer(provider, nil, usageLogger, nil, systemOneAliases, nil, nil, nil, nil)

	c, rec := echotest.Post(t, "/v1/systemone", systemOneBody("decider"))
	require.NoError(t, handler.SystemOne(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, systemOneAnswer, rec.Body.String())

	assert.Equal(t, "jev", provider.lastPassthroughProvider)
	assert.Equal(t, "systemone", provider.lastPassthroughReq.Endpoint)
	assert.Equal(t, "kev", provider.lastPassthroughReq.ProviderName)
	body := forwardedSystemOneBody(t, provider)
	assert.Equal(t, "kev-latest", body["model"])
	assert.Equal(t, "I was charged twice for card 4111.", body["state"])
	questions, err := json.Marshal(body["questions"])
	require.NoError(t, err)
	assert.JSONEq(t, systemOneQuestions, string(questions))

	require.Len(t, usageLogger.entries, 1)
	entry := usageLogger.entries[0]
	assert.Equal(t, 275, entry.InputTokens)
	assert.Equal(t, 20, entry.OutputTokens)
	assert.Equal(t, "/v1/systemone", entry.Endpoint)
	assert.Equal(t, "jev", entry.Provider)
	assert.Equal(t, "kev", entry.ProviderName)
	assert.Equal(t, "kev-1.0", entry.Model, "usage is recorded under the model that answered")
}

// OpenRouter serves Jev at the same path, so it is a native target too; its
// reported cost is kept with the usage entry.
func TestSystemOne_ForwardsOpenRouterJevNatively(t *testing.T) {
	answer := `{"id":"gen-dec-1","model":"typesafe/jev-1.13-20260917","answers":{},"usage":{"input_tokens":275,"output_tokens":20,"cost":0.00003}}`
	provider := newSystemOneProvider(answer)
	usageLogger := &collectingUsageLogger{config: usage.Config{Enabled: true}}
	handler := newHandlerWithAuthorizer(provider, nil, usageLogger, nil, nil, nil, nil, nil, nil)

	c, rec := echotest.Post(t, "/v1/systemone", systemOneBody("openrouter/typesafe/jev-1.13"))
	require.NoError(t, handler.SystemOne(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.Equal(t, "openrouter", provider.lastPassthroughProvider)
	assert.Equal(t, "typesafe/jev-1.13", forwardedSystemOneBody(t, provider)["model"])
	require.Len(t, usageLogger.entries, 1)
	assert.InDelta(t, 0.00003, usageLogger.entries[0].RawData["cost"], 1e-12)
}

// The endpoint never translates: a model on a provider without the System One
// API is rejected with an explanation, whether named directly or through a
// virtual model.
func TestSystemOne_RejectsModelsWithoutSystemOneAPI(t *testing.T) {
	for _, model := range []string{"openai/gpt-5-mini", "chatty"} {
		t.Run(model, func(t *testing.T) {
			provider := newSystemOneProvider(systemOneAnswer)
			handler := newHandlerWithAuthorizer(provider, nil, nil, nil, systemOneAliases, nil, nil, nil, nil)

			c, rec := echotest.Post(t, "/v1/systemone", systemOneBody(model))
			require.NoError(t, handler.SystemOne(c))

			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), "no System One API")
			assert.Contains(t, rec.Body.String(), "does not translate")
			assert.Nil(t, provider.lastPassthroughReq)
		})
	}
}

func TestSystemOne_RequiresModel(t *testing.T) {
	provider := newSystemOneProvider(systemOneAnswer)
	handler := NewHandler(provider, nil, nil, nil)

	c, rec := echotest.Post(t, "/v1/systemone", `{"state":"hi","questions":{}}`)
	require.NoError(t, handler.SystemOne(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "model is required")
}

// stateRedactingPatcher stands in for an anonymizing guardrail.
type stateRedactingPatcher struct{}

func (stateRedactingPatcher) PatchChatRequest(_ context.Context, req *core.ChatRequest) (*core.ChatRequest, error) {
	return req, nil
}

func (stateRedactingPatcher) PatchResponsesRequest(_ context.Context, req *core.ResponsesRequest) (*core.ResponsesRequest, error) {
	return req, nil
}

func (stateRedactingPatcher) PatchSystemOneRequest(_ context.Context, req *core.SystemOneRequest) (*core.SystemOneRequest, error) {
	patched := *req
	patched.State = json.RawMessage(strings.ReplaceAll(string(req.State), "4111", "[card]"))
	return &patched, nil
}

// Guardrails see the state and their edits reach the provider; the rest of
// the body is untouched.
func TestSystemOne_GuardrailsEditState(t *testing.T) {
	provider := newSystemOneProvider(systemOneAnswer)
	handler := newHandlerWithAuthorizer(provider, nil, nil, nil, systemOneAliases, nil, nil, nil, stateRedactingPatcher{})

	c, rec := echotest.Post(t, "/v1/systemone", systemOneBody("decider"))
	require.NoError(t, handler.SystemOne(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body := forwardedSystemOneBody(t, provider)
	assert.Equal(t, "I was charged twice for card [card].", body["state"])
	assert.Equal(t, "kev-latest", body["model"])
}

// Through the full middleware stack a System One call is audited under its
// own path with the requested and resolved routes, and its usage recorded.
func TestSystemOne_AuditsAndRecordsUsageThroughServer(t *testing.T) {
	provider := newSystemOneProvider(systemOneAnswer)
	auditLogger := &capturingAuditLogger{config: auditlog.Config{Enabled: true, LogBodies: true}}
	usageLogger := &collectingUsageLogger{config: usage.Config{Enabled: true}}
	srv := New(provider, &Config{
		AuditLogger:   auditLogger,
		UsageLogger:   usageLogger,
		ModelResolver: systemOneAliases,
	})

	rec := postJSON(t, srv, "/v1/systemone", systemOneBody("decider"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	require.Len(t, auditLogger.entries, 1)
	entry := auditLogger.entries[0]
	assert.Equal(t, "/v1/systemone", entry.Path)
	assert.Equal(t, http.StatusOK, entry.StatusCode)
	assert.Equal(t, "decider", entry.RequestedModel)
	assert.Equal(t, "kev/kev-latest", entry.ResolvedModel)
	assert.True(t, entry.AliasUsed)
	assert.Equal(t, "jev", entry.Provider)
	assert.Equal(t, "kev", entry.ProviderName)
	require.NotNil(t, entry.Data)
	requestBody, err := json.Marshal(entry.Data.RequestBody)
	require.NoError(t, err)
	assert.Contains(t, string(requestBody), "charged twice")
	responseBody, err := json.Marshal(entry.Data.ResponseBody)
	require.NoError(t, err)
	assert.Contains(t, string(responseBody), "noul")

	require.Len(t, usageLogger.entries, 1)
	assert.Equal(t, "/v1/systemone", usageLogger.entries[0].Endpoint)
	assert.Equal(t, entry.RequestID, usageLogger.entries[0].RequestID)
}

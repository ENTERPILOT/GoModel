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

// Without a provider that serves System One the endpoint does not exist,
// whatever the model.
func TestSystemOne_UnavailableWithoutSystemOneProvider(t *testing.T) {
	provider := &mockProvider{
		supportedModels: []string{"gpt-5-mini"},
		providerTypes:   map[string]string{"openai/gpt-5-mini": "openai"},
		providerNames:   map[string]string{"openai/gpt-5-mini": "openai"},
	}
	handler := NewHandler(provider, nil, nil, nil)

	c, rec := echotest.Post(t, "/v1/systemone", systemOneBody("gpt-5-mini"))
	require.NoError(t, handler.SystemOne(c))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "jev or openrouter provider")
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

// OpenRouter serves System One natively, so it enables the endpoint on its own.
// Its catalog names Jev "~typesafe/jev-latest"; a virtual model gives SDK
// callers the "jev-latest" name they send by default.
func TestSystemOne_WorksWithOpenRouterAlone(t *testing.T) {
	answer := `{"id":"gen-dec-1","model":"typesafe/jev-1.13-20260917","answers":{},"usage":{"input_tokens":10,"output_tokens":1}}`
	for _, model := range []string{"openrouter/~typesafe/jev-latest", "jev-latest"} {
		t.Run(model, func(t *testing.T) {
			provider := &mockProvider{
				supportedModels: []string{"~typesafe/jev-latest"},
				providerTypes:   map[string]string{"openrouter/~typesafe/jev-latest": "openrouter"},
				providerNames:   map[string]string{"openrouter/~typesafe/jev-latest": "openrouter"},
				passthroughResponse: &core.PassthroughResponse{
					StatusCode: http.StatusOK,
					Headers:    map[string][]string{"Content-Type": {"application/json"}},
					Body:       io.NopCloser(strings.NewReader(answer)),
				},
			}
			aliases := systemOneAliasResolver{"jev-latest": {Provider: "openrouter", Model: "~typesafe/jev-latest"}}
			handler := newHandlerWithAuthorizer(provider, nil, nil, nil, aliases, nil, nil, nil, nil)

			c, rec := echotest.Post(t, "/v1/systemone", systemOneBody(model))
			require.NoError(t, handler.SystemOne(c))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			assert.Equal(t, "openrouter", provider.lastPassthroughProvider)
			assert.Equal(t, "systemone", provider.lastPassthroughReq.Endpoint)
			assert.Equal(t, "~typesafe/jev-latest", forwardedSystemOneBody(t, provider)["model"])
		})
	}
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

// catalogProvider adds the router's single-model catalog lookup to the mock.
type catalogProvider struct {
	*mockProvider
	models map[string]core.Model
}

func (p catalogProvider) LookupModel(model string) (*core.Model, bool) {
	found, ok := p.models[model]
	return &found, ok
}

// OpenRouter serves chat and decision models from one provider, so the model
// itself must be a System One model: one catalogued with a generation mode is
// rejected, while a decision model (a utility model with no mode) is forwarded.
func TestSystemOne_RejectsOpenRouterChatModels(t *testing.T) {
	provider := catalogProvider{
		mockProvider: &mockProvider{
			supportedModels: []string{"typesafe/jev-1.13", "openai/gpt-4o-mini"},
			providerTypes: map[string]string{
				"openrouter/typesafe/jev-1.13":  "openrouter",
				"openrouter/openai/gpt-4o-mini": "openrouter",
			},
			providerNames: map[string]string{
				"openrouter/typesafe/jev-1.13":  "openrouter",
				"openrouter/openai/gpt-4o-mini": "openrouter",
			},
			passthroughResponse: &core.PassthroughResponse{
				StatusCode: http.StatusOK,
				Headers:    map[string][]string{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(systemOneAnswer)),
			},
		},
		models: map[string]core.Model{
			"openrouter/typesafe/jev-1.13": {ID: "typesafe/jev-1.13", Metadata: &core.ModelMetadata{
				Categories: []core.ModelCategory{core.CategoryUtility},
			}},
			"openrouter/openai/gpt-4o-mini": {ID: "openai/gpt-4o-mini", Metadata: &core.ModelMetadata{
				Modes: []string{"chat"}, Categories: []core.ModelCategory{core.CategoryTextGeneration},
			}},
		},
	}
	handler := NewHandler(provider, nil, nil, nil)

	c, rec := echotest.Post(t, "/v1/systemone", systemOneBody("openrouter/openai/gpt-4o-mini"))
	require.NoError(t, handler.SystemOne(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "is a chat model, not a System One model")
	assert.Contains(t, rec.Body.String(), "does not translate")
	assert.Nil(t, provider.lastPassthroughReq)

	c, rec = echotest.Post(t, "/v1/systemone", systemOneBody("openrouter/typesafe/jev-1.13"))
	require.NoError(t, handler.SystemOne(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "openrouter", provider.lastPassthroughProvider)
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

// inPlaceRedactingPatcher edits the request it was given and returns it; the
// patcher contract allows that, and the edit must still reach the provider.
type inPlaceRedactingPatcher struct{ stateRedactingPatcher }

func (inPlaceRedactingPatcher) PatchSystemOneRequest(_ context.Context, req *core.SystemOneRequest) (*core.SystemOneRequest, error) {
	req.State = json.RawMessage(strings.ReplaceAll(string(req.State), "4111", "[card]"))
	return req, nil
}

// Guardrails see the state and their edits reach the provider, whether the
// patcher returns a copy or edits in place; the rest of the body is untouched.
func TestSystemOne_GuardrailsEditState(t *testing.T) {
	patchers := map[string]TranslatedRequestPatcher{
		"copy":     stateRedactingPatcher{},
		"in place": inPlaceRedactingPatcher{},
	}
	for name, patcher := range patchers {
		t.Run(name, func(t *testing.T) {
			provider := newSystemOneProvider(systemOneAnswer)
			handler := newHandlerWithAuthorizer(provider, nil, nil, nil, systemOneAliases, nil, nil, nil, patcher)

			c, rec := echotest.Post(t, "/v1/systemone", systemOneBody("decider"))
			require.NoError(t, handler.SystemOne(c))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			body := forwardedSystemOneBody(t, provider)
			assert.Equal(t, "I was charged twice for card [card].", body["state"])
			assert.Equal(t, "kev-latest", body["model"])
		})
	}
}

// Through the full middleware stack an unavailable endpoint answers 404
// before the model is resolved, so a missing or unknown model is not
// reported for a route that is not there.
func TestSystemOne_UnavailableBeforeModelResolution(t *testing.T) {
	provider := &mockProvider{
		supportedModels: []string{"gpt-5-mini"},
		providerTypes:   map[string]string{"openai/gpt-5-mini": "openai"},
		providerNames:   map[string]string{"openai/gpt-5-mini": "openai"},
	}
	srv := New(provider, &Config{})

	for _, body := range []string{`{"state":"hi","questions":{}}`, systemOneBody("no-such-model")} {
		rec := postJSON(t, srv, "/v1/systemone", body)
		assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), "jev or openrouter provider")
	}
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

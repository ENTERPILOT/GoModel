package server

import (
	"context"
	"fmt"
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

const decisionQuestions = `[{"type":"predicate","name":"refund","instructions":"Is the customer asking for money back?"}]`

func decisionBody(model string) string {
	return `{"model":"` + model + `","input":"I was charged twice for card 4111.","questions":` + decisionQuestions + `,"prompt_cache_key":"support"}`
}

// scriptedDecisionsProvider serves Decisions for the given provider types,
// answering by the model each body forwards, and records every call as
// "<provider name> <provider type> <model>".
type scriptedDecisionsProvider struct {
	*mockProvider
	types []string
	// statuses maps a forwarded model to the status it answers with; 200 by
	// default.
	statuses map[string]int
	catalog  map[string]core.Model
	calls    []string
	bodies   []map[string]any
}

func (p *scriptedDecisionsProvider) LookupModel(model string) (*core.Model, bool) {
	found, ok := p.catalog[model]
	return &found, ok
}

func newScriptedDecisionsProvider(models map[string]string, types ...string) *scriptedDecisionsProvider {
	mock := &mockProvider{providerTypes: map[string]string{}, providerNames: map[string]string{}}
	for qualified, providerType := range models {
		providerName, model, _ := strings.Cut(qualified, "/")
		mock.supportedModels = append(mock.supportedModels, model)
		mock.providerTypes[qualified] = providerType
		mock.providerNames[qualified] = providerName
	}
	return &scriptedDecisionsProvider{mockProvider: mock, types: types, statuses: map[string]int{}}
}

func (p *scriptedDecisionsProvider) DecisionsProviderTypes() []string { return p.types }

func (p *scriptedDecisionsProvider) Decisions(_ context.Context, providerType string, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	var sent map[string]any
	if err := json.Unmarshal(raw, &sent); err != nil {
		return nil, err
	}
	model, _ := sent["model"].(string)
	p.calls = append(p.calls, req.ProviderName+" "+providerType+" "+model)
	p.bodies = append(p.bodies, sent)

	status := p.statuses[model]
	body := `{"error":{"message":"overloaded"}}`
	if status == 0 {
		status = http.StatusOK
		body = `{"answers":[{"name":"refund","type":"predicate","probability":0.98}],"model":"` + model + `-answered",` +
			`"usage":{"input_tokens":40,"input_tokens_details":{"cache_write_tokens":0,"cached_tokens":0},"output_tokens":0,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":40}}`
	}
	return &core.PassthroughResponse{
		StatusCode: status,
		Headers:    map[string][]string{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func newDecisionsTestProvider() *scriptedDecisionsProvider {
	return newScriptedDecisionsProvider(map[string]string{
		"openai/gpt-6-luna":            "openai",
		"kev/kev-latest":               "jev",
		"openrouter/typesafe/jev-1.13": "openrouter",
	}, "openai", "jev")
}

var decisionAliases = systemOneAliasResolver{
	"classifier": {Provider: "openai", Model: "gpt-6-luna"},
	"router":     {Provider: "openrouter", Model: "typesafe/jev-1.13"},
}

// Without a provider that serves Decisions the endpoint does not exist,
// whatever the model.
func TestDecisions_UnavailableWithoutDecisionsProvider(t *testing.T) {
	provider := newScriptedDecisionsProvider(map[string]string{"openrouter/typesafe/jev-1.13": "openrouter"})
	handler := NewHandler(provider, nil, nil, nil)

	c, rec := echotest.Post(t, "/v1/decisions", decisionBody("openrouter/typesafe/jev-1.13"))
	require.NoError(t, handler.Decisions(c))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "openai or jev provider")
	assert.Empty(t, provider.calls)
}

// A virtual model resolves to its target, and the body reaches the provider
// unchanged except for the concrete model name; usage is recorded under the
// model that answered.
func TestDecisions_ForwardsAndRecordsUsage(t *testing.T) {
	provider := newDecisionsTestProvider()
	usageLogger := &collectingUsageLogger{config: usage.Config{Enabled: true}}
	handler := newHandlerWithAuthorizer(provider, nil, usageLogger, nil, decisionAliases, nil, nil, nil, nil)

	c, rec := echotest.Post(t, "/v1/decisions", decisionBody("classifier"))
	require.NoError(t, handler.Decisions(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"probability":0.98`)

	assert.Equal(t, []string{"openai openai gpt-6-luna"}, provider.calls)
	sent := provider.bodies[0]
	assert.Equal(t, "I was charged twice for card 4111.", sent["input"])
	assert.Equal(t, "support", sent["prompt_cache_key"])
	questions, err := json.Marshal(sent["questions"])
	require.NoError(t, err)
	assert.JSONEq(t, decisionQuestions, string(questions))

	require.Len(t, usageLogger.entries, 1)
	entry := usageLogger.entries[0]
	assert.Equal(t, 40, entry.InputTokens)
	assert.Equal(t, "/v1/decisions", entry.Endpoint)
	assert.Equal(t, "openai", entry.Provider)
	assert.Equal(t, "gpt-6-luna-answered", entry.Model)
}

// A jev model is routed to its named provider instance, which translates
// the request to System One.
func TestDecisions_RoutesJevModelsToTheirProvider(t *testing.T) {
	provider := newDecisionsTestProvider()
	handler := NewHandler(provider, nil, nil, nil)

	c, rec := echotest.Post(t, "/v1/decisions", decisionBody("kev/kev-latest"))
	require.NoError(t, handler.Decisions(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"kev jev kev-latest"}, provider.calls)
}

// A model on a provider without a Decisions API is rejected with an
// explanation, whether named directly or through a virtual model.
func TestDecisions_RejectsModelsWithoutDecisionsAPI(t *testing.T) {
	for _, model := range []string{"openrouter/typesafe/jev-1.13", "router"} {
		t.Run(model, func(t *testing.T) {
			provider := newDecisionsTestProvider()
			handler := newHandlerWithAuthorizer(provider, nil, nil, nil, decisionAliases, nil, nil, nil, nil)

			c, rec := echotest.Post(t, "/v1/decisions", decisionBody(model))
			require.NoError(t, handler.Decisions(c))

			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), "provider type openrouter, which has no decisions API")
			assert.Empty(t, provider.calls)
		})
	}
}

func TestDecisions_RequiresModel(t *testing.T) {
	provider := newDecisionsTestProvider()
	handler := NewHandler(provider, nil, nil, nil)

	c, rec := echotest.Post(t, "/v1/decisions", `{"input":"hi","questions":[]}`)
	require.NoError(t, handler.Decisions(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "model is required")
}

// inputRedactingPatcher stands in for an anonymizing guardrail.
type inputRedactingPatcher struct{ stateRedactingPatcher }

func (inputRedactingPatcher) PatchDecisionRequest(_ context.Context, req *core.DecisionRequest) (*core.DecisionRequest, error) {
	patched := *req
	patched.Input = json.RawMessage(strings.ReplaceAll(string(req.Input), "4111", "[card]"))
	return &patched, nil
}

// Guardrails see the input and their edits reach the provider; the rest of
// the body is untouched.
func TestDecisions_GuardrailsEditInput(t *testing.T) {
	provider := newDecisionsTestProvider()
	handler := newHandlerWithAuthorizer(provider, nil, nil, nil, decisionAliases, nil, nil, nil, inputRedactingPatcher{})

	c, rec := echotest.Post(t, "/v1/decisions", decisionBody("classifier"))
	require.NoError(t, handler.Decisions(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	sent := provider.bodies[0]
	assert.Equal(t, "I was charged twice for card [card].", sent["input"])
	assert.Equal(t, "gpt-6-luna", sent["model"])
	assert.Equal(t, "support", sent["prompt_cache_key"])
}

// Failover moves between providers with a Decisions API, across APIs: a
// failing Kev server falls over to OpenAI, skipping OpenRouter, which has
// none.
func TestDecisions_FailsOverToTheNextDecisionsTarget(t *testing.T) {
	provider := newDecisionsTestProvider()
	provider.statuses["kev-latest"] = http.StatusServiceUnavailable
	handler := newHandler(provider, nil, nil, nil, nil, nil, failoverResolverStub{selectors: []core.ModelSelector{
		{Provider: "openrouter", Model: "typesafe/jev-1.13"},
		{Provider: "openai", Model: "gpt-6-luna"},
	}}, nil)

	entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
	c, rec := echotest.Post(t, "/v1/decisions", decisionBody("kev/kev-latest"), echotest.WithValue(string(auditlog.LogEntryKey), entry))
	require.NoError(t, handler.Decisions(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.Equal(t, []string{"kev jev kev-latest", "openai openai gpt-6-luna"}, provider.calls)
	require.NotNil(t, entry.Data.Failover)
	assert.Equal(t, "openai/gpt-6-luna", entry.Data.Failover.TargetModel)
}

// Through the full middleware stack an unavailable endpoint answers 404
// before the model is resolved, and a served call is audited and its usage
// recorded under /v1/decisions.
func TestDecisions_ThroughServer(t *testing.T) {
	t.Run("unavailable", func(t *testing.T) {
		provider := newScriptedDecisionsProvider(map[string]string{"openrouter/typesafe/jev-1.13": "openrouter"})
		srv := New(provider, &Config{})

		for _, body := range []string{`{"input":"hi","questions":[]}`, decisionBody("no-such-model")} {
			rec := postJSON(t, srv, "/v1/decisions", body)
			assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		}
	})

	t.Run("served", func(t *testing.T) {
		provider := newDecisionsTestProvider()
		auditLogger := &capturingAuditLogger{config: auditlog.Config{Enabled: true, LogBodies: true}}
		usageLogger := &collectingUsageLogger{config: usage.Config{Enabled: true}}
		srv := New(provider, &Config{AuditLogger: auditLogger, UsageLogger: usageLogger, ModelResolver: decisionAliases})

		rec := postJSON(t, srv, "/v1/decisions", decisionBody("classifier"))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		require.Len(t, auditLogger.entries, 1)
		entry := auditLogger.entries[0]
		assert.Equal(t, "/v1/decisions", entry.Path)
		assert.Equal(t, "classifier", entry.RequestedModel)
		assert.Equal(t, "openai/gpt-6-luna", entry.ResolvedModel)
		assert.Equal(t, "openai", entry.Provider)

		require.Len(t, usageLogger.entries, 1)
		assert.Equal(t, "/v1/decisions", usageLogger.entries[0].Endpoint)
		assert.Equal(t, entry.RequestID, usageLogger.entries[0].RequestID)
	})
}

const decisionImageBody = `{"model":"%s","input":[{"role":"user","content":[` +
	`{"type":"input_text","text":"Is the box damaged?"},{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo="}]}],` +
	`"questions":[{"type":"predicate","name":"damaged","instructions":"Damaged?"}]}`

// newVisionDecisionsProvider adds hosted Jev, which reads images, next to a
// text-only Kev server, as the jev provider catalogs them.
func newVisionDecisionsProvider() *scriptedDecisionsProvider {
	provider := newScriptedDecisionsProvider(map[string]string{
		"openai/gpt-6-luna": "openai",
		"jev/jev-latest":    "jev",
		"kev/kev-latest":    "jev",
	}, "openai", "jev")
	provider.catalog = map[string]core.Model{
		"jev/jev-latest":    {ID: "jev-latest", Metadata: &core.ModelMetadata{Capabilities: map[string]bool{"vision": true}}},
		"kev/kev-latest":    {ID: "kev-latest", Metadata: &core.ModelMetadata{Capabilities: map[string]bool{"vision": false}}},
		"openai/gpt-6-luna": {ID: "gpt-6-luna", Metadata: &core.ModelMetadata{Capabilities: map[string]bool{"vision": true}}},
	}
	return provider
}

// An image request to a model that reads text only is refused before it is
// sent: a Kev server would read the image references as text.
func TestDecisions_RefusesImagesForTextOnlyModels(t *testing.T) {
	provider := newVisionDecisionsProvider()
	handler := NewHandler(provider, nil, nil, nil)

	c, rec := echotest.Post(t, "/v1/decisions", fmt.Sprintf(decisionImageBody, "kev/kev-latest"))
	require.NoError(t, handler.Decisions(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "does not read images")
	assert.Empty(t, provider.calls)

	c, rec = echotest.Post(t, "/v1/decisions", fmt.Sprintf(decisionImageBody, "jev/jev-latest"))
	require.NoError(t, handler.Decisions(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"jev jev jev-latest"}, provider.calls)
}

// Failover skips a target that cannot read the input: a request with images
// moves past the text-only Kev server to the next target that reads images,
// while a text request may still use it.
func TestDecisions_FailoverSkipsTargetsThatCannotReadImages(t *testing.T) {
	failovers := failoverResolverStub{selectors: []core.ModelSelector{
		{Provider: "kev", Model: "kev-latest"},
		{Provider: "openai", Model: "gpt-6-luna"},
	}}

	provider := newVisionDecisionsProvider()
	provider.statuses["jev-latest"] = http.StatusServiceUnavailable
	handler := newHandler(provider, nil, nil, nil, nil, nil, failovers, nil)
	c, rec := echotest.Post(t, "/v1/decisions", fmt.Sprintf(decisionImageBody, "jev/jev-latest"))
	require.NoError(t, handler.Decisions(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"jev jev jev-latest", "openai openai gpt-6-luna"}, provider.calls)

	provider = newVisionDecisionsProvider()
	provider.statuses["jev-latest"] = http.StatusServiceUnavailable
	handler = newHandler(provider, nil, nil, nil, nil, nil, failovers, nil)
	c, rec = echotest.Post(t, "/v1/decisions", decisionBody("jev/jev-latest"))
	require.NoError(t, handler.Decisions(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"jev jev jev-latest", "kev jev kev-latest"}, provider.calls)
}

// A chain whose first target reads text only still serves images: the
// request starts at the first failover target that reads them, while text
// requests keep the first target. The promoted target is the one audited.
func TestDecisions_ImagesStartAtTheFirstTargetThatReadsThem(t *testing.T) {
	failovers := failoverResolverStub{selectors: []core.ModelSelector{{Provider: "openai", Model: "gpt-6-luna"}}}

	provider := newVisionDecisionsProvider()
	handler := newHandler(provider, nil, nil, nil, nil, nil, failovers, nil)
	entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
	c, rec := echotest.Post(t, "/v1/decisions", fmt.Sprintf(decisionImageBody, "kev/kev-latest"), echotest.WithValue(string(auditlog.LogEntryKey), entry))
	require.NoError(t, handler.Decisions(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"openai openai gpt-6-luna"}, provider.calls)
	assert.Equal(t, "openai/gpt-6-luna", entry.ResolvedModel)

	c, rec = echotest.Post(t, "/v1/decisions", decisionBody("kev/kev-latest"))
	require.NoError(t, handler.Decisions(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"openai openai gpt-6-luna", "kev jev kev-latest"}, provider.calls)
}

// An image link sent to a jev model, which takes images inline only, says so.
func TestDecisions_ImageLinksToJevAskForInlineData(t *testing.T) {
	provider := newVisionDecisionsProvider()
	handler := NewHandler(provider, nil, nil, nil)
	body := `{"model":"jev/jev-latest","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.com/a.png"}]}],"questions":[{"type":"predicate","instructions":"?"}]}`

	c, rec := echotest.Post(t, "/v1/decisions", body)
	require.NoError(t, handler.Decisions(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "not a link")
	assert.Empty(t, provider.calls)
}

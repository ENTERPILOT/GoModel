package server

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/cache"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/gateway"
	"github.com/enterpilot/gomodel/internal/responsecache"
	"github.com/enterpilot/gomodel/internal/usage"
)

// scriptedSystemOneProvider answers each passthrough by the model the body
// forwards, and records every call as "<provider name> <endpoint> <model>".
type scriptedSystemOneProvider struct {
	*mockProvider
	// statuses maps a forwarded model to the status it answers with;
	// 200 by default.
	statuses map[string]int
	catalog  map[string]core.Model
	calls    []string
	// idempotencyKeys records the explicit Idempotency-Key of each call.
	idempotencyKeys []string
}

// newScriptedSystemOneProvider configures the given "<provider>/<model>"
// selectors, keyed to their provider types.
func newScriptedSystemOneProvider(models map[string]string) *scriptedSystemOneProvider {
	mock := &mockProvider{providerTypes: map[string]string{}, providerNames: map[string]string{}}
	for qualified, providerType := range models {
		providerName, model, _ := strings.Cut(qualified, "/")
		mock.supportedModels = append(mock.supportedModels, model)
		mock.providerTypes[qualified] = providerType
		mock.providerNames[qualified] = providerName
	}
	return &scriptedSystemOneProvider{mockProvider: mock, statuses: map[string]int{}}
}

func (p *scriptedSystemOneProvider) Passthrough(_ context.Context, _ string, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	var sent struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(raw, &sent); err != nil {
		return nil, err
	}
	p.calls = append(p.calls, req.ProviderName+" "+req.Endpoint+" "+sent.Model)
	p.idempotencyKeys = append(p.idempotencyKeys, req.Headers.Get(core.IdempotencyKeyHeader))

	status := p.statuses[sent.Model]
	body := `{"error":{"message":"overloaded"}}`
	if status == 0 {
		status = http.StatusOK
		body = `{"model":"` + sent.Model + `-answered","answers":{},"usage":{"input_tokens":10,"output_tokens":1}}`
	}
	return &core.PassthroughResponse{
		StatusCode: status,
		Headers:    map[string][]string{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func (p *scriptedSystemOneProvider) LookupModel(model string) (*core.Model, bool) {
	found, ok := p.catalog[model]
	return &found, ok
}

func (p *scriptedSystemOneProvider) ProviderNamesForType(providerType string) []string {
	var names []string
	for qualified, candidate := range p.providerTypes {
		if name := p.providerNames[qualified]; candidate == providerType && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func systemOneRequest(model string) string {
	return `{"model":"` + model + `","state":"I was charged twice.","questions":{"refund":{"type":"noul","instructions":"Refund?"}}}`
}

// An identical request is answered from the exact cache without reaching the
// provider.
func TestSystemOne_ServesRepeatsFromTheExactCache(t *testing.T) {
	provider := newScriptedSystemOneProvider(map[string]string{"kev/kev-latest": "jev"})
	store := cache.NewMapStore()
	defer store.Close()
	mw := responsecache.NewResponseCacheMiddlewareWithStore(store, time.Hour)
	handler := NewHandler(provider, nil, nil, nil)
	handler.responseCache = mw

	c, first := echotest.Post(t, "/v1/systemone", systemOneRequest("kev-latest"))
	require.NoError(t, handler.SystemOne(c))
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	// The cache write is asynchronous; drain it before the repeat.
	require.NoError(t, mw.Close())

	c, second := echotest.Post(t, "/v1/systemone", systemOneRequest("kev-latest"))
	require.NoError(t, handler.SystemOne(c))
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())

	assert.Equal(t, "HIT (exact)", second.Header().Get("X-Cache"))
	assert.JSONEq(t, first.Body.String(), second.Body.String())
	assert.Len(t, provider.calls, 1, "the repeat must not reach the provider")
}

// A different state is a different decision: it misses the cache.
func TestSystemOne_CacheKeyCoversTheState(t *testing.T) {
	provider := newScriptedSystemOneProvider(map[string]string{"kev/kev-latest": "jev"})
	store := cache.NewMapStore()
	defer store.Close()
	mw := responsecache.NewResponseCacheMiddlewareWithStore(store, time.Hour)
	handler := NewHandler(provider, nil, nil, nil)
	handler.responseCache = mw

	c, rec := echotest.Post(t, "/v1/systemone", systemOneRequest("kev-latest"))
	require.NoError(t, handler.SystemOne(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, mw.Close())

	c, rec = echotest.Post(t, "/v1/systemone", strings.Replace(systemOneRequest("kev-latest"), "twice", "once", 1))
	require.NoError(t, handler.SystemOne(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.Empty(t, rec.Header().Get("X-Cache"))
	assert.Len(t, provider.calls, 2)
}

// When the primary fails with an availability error, the request moves to
// the virtual model's next target in its own dialect. A target without the
// System One API is skipped rather than called, and usage and audit carry the
// model that answered.
func TestSystemOne_FailsOverToTheNextSystemOneTarget(t *testing.T) {
	provider := newScriptedSystemOneProvider(map[string]string{
		"kev/kev-latest":               "jev",
		"openai/gpt-5-mini":            "openai",
		"openrouter/typesafe/jev-1.13": "openrouter",
	})
	provider.statuses["kev-latest"] = http.StatusServiceUnavailable
	usageLogger := &collectingUsageLogger{config: usage.Config{Enabled: true}}
	handler := newHandler(provider, nil, usageLogger, nil, nil, nil, failoverResolverStub{selectors: []core.ModelSelector{
		{Provider: "openai", Model: "gpt-5-mini"},
		{Provider: "openrouter", Model: "typesafe/jev-1.13"},
	}}, nil)

	entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
	c, rec := echotest.Post(t, "/v1/systemone", systemOneRequest("kev/kev-latest"), echotest.WithValue(string(auditlog.LogEntryKey), entry))
	require.NoError(t, handler.SystemOne(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.Equal(t, []string{"kev systemone kev-latest", "openrouter systemone typesafe/jev-1.13"}, provider.calls,
		"the chat model must be skipped, not called")
	require.NotNil(t, entry.Data.Failover)
	assert.Equal(t, "openrouter/typesafe/jev-1.13", entry.Data.Failover.TargetModel)
	assert.Equal(t, "openrouter/typesafe/jev-1.13", entry.ResolvedModel)
	assert.Equal(t, "openrouter", entry.Provider)
	require.Len(t, entry.Data.Attempts, 2, "a skipped target is not an attempt")
	assert.Equal(t, http.StatusServiceUnavailable, entry.Data.Attempts[0].StatusCode)
	assert.True(t, entry.Data.Attempts[1].Success)

	require.Len(t, usageLogger.entries, 1)
	assert.Equal(t, "openrouter", usageLogger.entries[0].Provider)
	assert.Equal(t, "typesafe/jev-1.13-answered", usageLogger.entries[0].Model)
}

// A skipped target does not count against max_attempts, so a chat model ahead
// of a valid target in the chain cannot use up the only failover attempt. The
// client's Idempotency-Key is not forwarded as a header: it reaches the
// primary through the request context, and a failover target's different
// body must not carry it.
func TestSystemOne_FailoverSkipsTargetsWithoutUsingAttempts(t *testing.T) {
	provider := newScriptedSystemOneProvider(map[string]string{
		"kev/kev-latest":               "jev",
		"openai/gpt-5-mini":            "openai",
		"openrouter/typesafe/jev-1.13": "openrouter",
	})
	provider.statuses["kev-latest"] = http.StatusServiceUnavailable
	handler := newHandler(provider, nil, nil, nil, nil, nil, failoverResolverStub{selectors: []core.ModelSelector{
		{Provider: "openai", Model: "gpt-5-mini"},
		{Provider: "openrouter", Model: "typesafe/jev-1.13"},
	}}, nil)
	handler.failoverPolicy = &gateway.FailoverPolicy{MaxAttempts: 1}

	c, rec := echotest.Post(t, "/v1/systemone", systemOneRequest("kev/kev-latest"), echotest.WithHeader(core.IdempotencyKeyHeader, "client-key-1"))
	require.NoError(t, handler.SystemOne(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.Equal(t, []string{"kev systemone kev-latest", "openrouter systemone typesafe/jev-1.13"}, provider.calls)
	assert.Equal(t, []string{"", ""}, provider.idempotencyKeys, "the key must not travel as an explicit header")
}

// A client error such as a malformed question is not an availability
// problem: it is returned as the upstream reported it, without failover.
func TestSystemOne_DoesNotFailOverOnClientErrors(t *testing.T) {
	provider := newScriptedSystemOneProvider(map[string]string{
		"kev/kev-latest":               "jev",
		"openrouter/typesafe/jev-1.13": "openrouter",
	})
	provider.statuses["kev-latest"] = http.StatusUnprocessableEntity
	handler := newHandler(provider, nil, nil, nil, nil, nil, failoverResolverStub{selectors: []core.ModelSelector{
		{Provider: "openrouter", Model: "typesafe/jev-1.13"},
	}}, nil)

	c, rec := echotest.Post(t, "/v1/systemone", systemOneRequest("kev/kev-latest"))
	require.NoError(t, handler.SystemOne(c))

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"kev systemone kev-latest"}, provider.calls)
}

// Kev's diagnostic routes are served natively on jev providers and refused
// on OpenRouter, which serves only the evaluation route.
func TestSystemOne_KevDiagnosticRoutes(t *testing.T) {
	provider := newScriptedSystemOneProvider(map[string]string{
		"kev/kev-latest":               "jev",
		"openrouter/typesafe/jev-1.13": "openrouter",
	})
	handler := NewHandler(provider, nil, nil, nil)

	for path, serve := range map[string]func(*echo.Context) error{
		"/v1/systemone/permute":  handler.SystemOnePermute,
		"/v1/systemone/separate": handler.SystemOneSeparate,
	} {
		t.Run(path, func(t *testing.T) {
			provider.calls = nil
			c, rec := echotest.Post(t, path, systemOneRequest("kev/kev-latest"))
			require.NoError(t, serve(c))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, []string{"kev " + strings.TrimPrefix(path, "/v1/") + " kev-latest"}, provider.calls)

			c, rec = echotest.Post(t, path, systemOneRequest("openrouter/typesafe/jev-1.13"))
			require.NoError(t, serve(c))
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), "this route is served by Kev servers")
		})
	}
}

// TypeSafe lists only its aliases but accepts any versioned ID, so a pinned
// version routes to a jev provider without being declared: named with its
// provider, bare when one jev provider is configured, or through a virtual
// model. A bare name stays unrouted when several jev providers could own it.
func TestSystemOne_RoutesUnlistedPinnedVersions(t *testing.T) {
	aliases := systemOneAliasResolver{"pinned": {Provider: "jev", Model: "jev-1.13.0"}}
	tests := []struct {
		name     string
		models   map[string]string
		model    string
		wantCall string
		wantCode int
	}{
		{name: "provider-qualified", models: map[string]string{"jev/jev-latest": "jev"}, model: "jev/jev-1.13.0", wantCall: "jev systemone jev-1.13.0"},
		{name: "bare with one jev provider", models: map[string]string{"jev/jev-latest": "jev", "openrouter/typesafe/jev-1.13": "openrouter"}, model: "jev-1.13.0", wantCall: "jev systemone jev-1.13.0"},
		{name: "virtual model", models: map[string]string{"jev/jev-latest": "jev"}, model: "pinned", wantCall: "jev systemone jev-1.13.0"},
		{name: "self-hosted provider name", models: map[string]string{"kev/kev-latest": "jev"}, model: "kev/kev-4b-2026-09", wantCall: "kev systemone kev-4b-2026-09"},
		{name: "bare with two jev providers", models: map[string]string{"jev/jev-latest": "jev", "kev/kev-latest": "jev"}, model: "jev-1.13.0", wantCode: http.StatusNotFound},
		{name: "unknown on OpenRouter", models: map[string]string{"openrouter/typesafe/jev-1.13": "openrouter"}, model: "openrouter/typesafe/jev-9", wantCode: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := newScriptedSystemOneProvider(tt.models)
			handler := newHandler(provider, nil, nil, nil, aliases, nil, nil, nil)

			c, rec := echotest.Post(t, "/v1/systemone", systemOneRequest(tt.model))
			require.NoError(t, handler.SystemOne(c))

			if tt.wantCode != 0 {
				assert.Equal(t, tt.wantCode, rec.Code, rec.Body.String())
				assert.Empty(t, provider.calls)
				return
			}
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, []string{tt.wantCall}, provider.calls)
		})
	}
}

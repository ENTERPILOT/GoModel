//go:build e2e

package e2e

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/ratelimit"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

// staticFailoverResolver returns a fixed selector list, standing in for the
// failover rules service.
type staticFailoverResolver struct {
	selectors []core.ModelSelector
}

func (r staticFailoverResolver) ResolveFailovers(_ *core.RequestModelResolution, _ core.Operation) []core.ModelSelector {
	return r.selectors
}

func sendModelChatRequest(t *testing.T, serverURL, model, message, requestID string) *http.Response {
	t.Helper()
	return sendBudgetJSONRequestWithHeaders(t, http.MethodPost, serverURL+chatCompletionsPath, core.ChatRequest{
		Model:    model,
		Messages: []core.Message{{Role: "user", Content: message}},
	}, map[string]string{
		"X-Request-ID":        requestID,
		"X-GoModel-User-Path": "/team/failover",
	})
}

// recordedChatModels decodes the model of every chat request the mock served,
// with provider qualification stripped.
func recordedChatModels(t *testing.T) []string {
	t.Helper()
	models := make([]string, 0)
	for _, recorded := range mockServer.Requests() {
		var req core.ChatRequest
		require.NoError(t, json.Unmarshal(recorded.Body, &req))
		models = append(models, strings.TrimPrefix(req.Model, "test/"))
	}
	return models
}

// A rate-saturated primary route with configured failover rules must be served
// by the failover target: the saturated provider is never called and the
// client sees no 429.
func TestRateLimitSaturatedPrimaryFailsOver_E2E(t *testing.T) {
	mockServer.ResetRequests()
	service := setupRateLimitService(t, []ratelimit.Rule{{
		Scope:         ratelimit.ScopeModel,
		Subject:       "gpt-4",
		PeriodSeconds: ratelimit.PeriodMinuteSeconds,
		MaxRequests:   new(int64(1)),
		Source:        ratelimit.SourceManual,
	}})

	ts := httptest.NewServer(setupE2EServer(t, e2eServerOptions{
		rateLimiter: service,
		failoverResolver: staticFailoverResolver{selectors: []core.ModelSelector{
			{Provider: "test", Model: "gpt-3.5-turbo"},
		}},
	}))
	defer ts.Close()

	// The first request has capacity and is served by the primary model.
	first := sendModelChatRequest(t, ts.URL, "gpt-4", "primary ok", "fo-primary")
	require.Equal(t, http.StatusOK, first.StatusCode)
	closeBody(first)

	// The second request saturates the model rule. Instead of a 429 it must
	// be served by the failover target, without touching the primary again.
	second := sendModelChatRequest(t, ts.URL, "gpt-4", "failover serves", "fo-failover")
	require.Equal(t, http.StatusOK, second.StatusCode, "saturated primary with failover configured must not 429")
	closeBody(second)

	models := recordedChatModels(t)
	require.Equal(t, []string{"gpt-4", "gpt-3.5-turbo"}, models,
		"first request served by the primary, second by the failover target; the saturated primary must not be called again")
}

// When a virtual model's targets are all rate-saturated and no failover is
// configured, the client receives the honest 429 with Retry-After — not an
// unavailable-model error — and the blocked request never reaches upstream.
func TestRateLimitFullySaturatedAliasReturns429_E2E(t *testing.T) {
	mockServer.ResetRequests()
	registry := setupE2ERegistry(t, "")

	// Provider-scoped rule: two requests fit, the third saturates everything
	// routed to the test provider — both alias targets at once.
	service := setupRateLimitService(t, []ratelimit.Rule{{
		Scope:         ratelimit.ScopeProvider,
		Subject:       "test",
		PeriodSeconds: ratelimit.PeriodMinuteSeconds,
		MaxRequests:   new(int64(2)),
		Source:        ratelimit.SourceManual,
	}})

	// Virtual model with two targets, wired exactly like the app: the
	// registry is the catalog and load balancing consults live rate-limit
	// capacity through the probe.
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	vmStore, err := virtualmodels.NewSQLiteStore(db)
	require.NoError(t, err)
	vmService, err := virtualmodels.NewService(vmStore, registry, true)
	require.NoError(t, err)
	vmService.SetTargetCapacity(func(qualifiedModel string) bool {
		return service.RouteAvailable(registry.GetProviderName(qualifiedModel), qualifiedModel)
	})
	require.NoError(t, vmService.Upsert(t.Context(), virtualmodels.VirtualModel{
		Source:   "balanced-chat",
		Strategy: virtualmodels.StrategyRoundRobin,
		Targets: []virtualmodels.Target{
			{Provider: "test", Model: "gpt-4"},
			{Provider: "test", Model: "gpt-3.5-turbo"},
		},
		Enabled: true,
	}))

	ts := httptest.NewServer(setupE2EServer(t, e2eServerOptions{
		registry:      registry,
		rateLimiter:   service,
		modelResolver: vmService,
	}))
	defer ts.Close()

	// Two requests round-robin across the targets while capacity lasts.
	for i, requestID := range []string{"alias-1", "alias-2"} {
		resp := sendModelChatRequest(t, ts.URL, "balanced-chat", "alias ok", requestID)
		require.Equal(t, http.StatusOK, resp.StatusCode, "request %d", i+1)
		closeBody(resp)
	}
	require.Equal(t, []string{"gpt-4", "gpt-3.5-turbo"}, recordedChatModels(t),
		"round robin must alternate targets while capacity lasts")

	// Every target is now saturated: the alias must still resolve and the
	// client must get the rate-limit answer, not an unavailable-model error.
	blocked := sendModelChatRequest(t, ts.URL, "balanced-chat", "alias blocked", "alias-3")
	defer closeBody(blocked)
	require.Equal(t, http.StatusTooManyRequests, blocked.StatusCode,
		"fully saturated alias must return 429, not an unavailable-model error")
	require.NotEmpty(t, blocked.Header.Get("Retry-After"))

	var envelope core.OpenAIErrorEnvelope
	require.NoError(t, json.NewDecoder(blocked.Body).Decode(&envelope))
	require.NotNil(t, envelope.Error.Code)
	require.Equal(t, "rate_limit_exceeded", *envelope.Error.Code)
	require.Contains(t, envelope.Error.Message, "provider test")
	require.Len(t, mockServer.Requests(), 2, "blocked request must not reach upstream")
}

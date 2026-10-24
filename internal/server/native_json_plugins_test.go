package server

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/cache"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/responsecache"

	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

// A response-phase guardrail selected for a decision route does not run
// there, and says so once per route rather than on every request.
func TestWarnSkippedResponsePlugins_WarnsOncePerRouteAndGuardrail(t *testing.T) {
	var buf bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })

	chains := phaseChains(t, map[string]string{"response": "warn"}, guardrails.StepReference{Ref: "phase", Phase: pluginapi.KindResponse, Step: 1})
	svc := &translatedInferenceService{provider: &capturingProvider{}, pluginChains: staticChainsResolver{chains: chains}}
	route := nativeJSONRoute{path: "/v1/decisions-warn-test", endpoint: "decisions", operation: "decisions"}

	svc.warnSkippedResponsePlugins(context.Background(), route)
	svc.warnSkippedResponsePlugins(context.Background(), route)
	assert.Equal(t, 1, strings.Count(buf.String(), "do not run on decision answers"))
	assert.Contains(t, buf.String(), "guardrail=phase")
	assert.Contains(t, buf.String(), "phase=response")

	buf.Reset()
	svc.pluginChains = staticChainsResolver{chains: &plugins.Chains{}}
	svc.warnSkippedResponsePlugins(context.Background(), nativeJSONRoute{path: "/v1/systemone-warn-test"})
	assert.Empty(t, buf.String(), "a workflow without response-phase guardrails logs nothing")
}

// A stream-phase guardrail is named as one, not as a response-phase one.
func TestWarnSkippedResponsePlugins_NamesThePhase(t *testing.T) {
	var buf bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })

	chains := phaseChains(t, map[string]string{"stream": "replace"}, guardrails.StepReference{Ref: "phase", Phase: pluginapi.KindStream, Step: 1})
	svc := &translatedInferenceService{provider: &capturingProvider{}, pluginChains: staticChainsResolver{chains: chains}}
	svc.warnSkippedResponsePlugins(context.Background(), nativeJSONRoute{path: "/v1/decisions-stream-test"})
	assert.Contains(t, buf.String(), "phase=stream")
	assert.NotContains(t, buf.String(), "phase=response")
}

// An answer replayed from the exact cache skips response guardrails as much
// as a live one, so it warns too: after a restart against a warm shared
// cache, the first request may well be a hit.
func TestWarnSkippedResponsePlugins_WarnsOnCacheHits(t *testing.T) {
	var buf bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })

	chains := phaseChains(t, map[string]string{"response": "warn"}, guardrails.StepReference{Ref: "phase", Phase: pluginapi.KindResponse, Step: 1})
	provider := newScriptedSystemOneProvider(map[string]string{"kev/kev-latest": "jev"})
	store := cache.NewMapStore()
	defer store.Close()
	mw := responsecache.NewResponseCacheMiddlewareWithStore(store, time.Hour)
	handler := newHandler(provider, nil, nil, nil, nil, nil, nil, guardrails.NewWorkflowRequestPatcher(staticChainsResolver{chains: chains}))
	handler.pluginChains = staticChainsResolver{chains: chains}
	handler.responseCache = mw

	c, first := echotest.Post(t, "/v1/systemone", systemOneRequest("kev-latest"))
	require.NoError(t, handler.SystemOne(c))
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.NoError(t, mw.Close())

	// Forget the warnings, as a restarted gateway would.
	skippedResponsePluginsWarned.Range(func(key, _ any) bool {
		skippedResponsePluginsWarned.Delete(key)
		return true
	})
	buf.Reset()
	c, second := echotest.Post(t, "/v1/systemone", systemOneRequest("kev-latest"))
	require.NoError(t, handler.SystemOne(c))
	require.Equal(t, "HIT (exact)", second.Header().Get("X-Cache"))
	assert.Contains(t, buf.String(), "do not run on decision answers")
	assert.Len(t, provider.calls, 1)
}

package server

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

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
	assert.Equal(t, 1, strings.Count(buf.String(), "response-phase guardrails do not run on decision answers"))
	assert.Contains(t, buf.String(), "guardrail=phase")

	buf.Reset()
	svc.pluginChains = staticChainsResolver{chains: &plugins.Chains{}}
	svc.warnSkippedResponsePlugins(context.Background(), nativeJSONRoute{path: "/v1/systemone-warn-test"})
	assert.Empty(t, buf.String(), "a workflow without response-phase guardrails logs nothing")
}

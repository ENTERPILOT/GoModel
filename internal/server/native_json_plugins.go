package server

import (
	"context"
	"log/slog"
	"sync"

	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

// skippedResponsePluginsWarned records the route, phase, and guardrail
// triples already warned about, so a guardrail scoped to every model is
// reported once per decision route rather than on every request.
var skippedResponsePluginsWarned sync.Map

// warnSkippedResponsePlugins logs the response and stream phase guardrails
// the workflow selects for a decision route, once per route, phase, and
// guardrail. They do not run there: a decision answer is typed values
// (probabilities, choices, scores), not a completion those phases inspect, so
// it is relayed unchanged. Without the warning, a guardrail scoped to every
// model would look as if it covered decision answers too.
func (s *translatedInferenceService) warnSkippedResponsePlugins(ctx context.Context, route nativeJSONRoute) {
	chains := s.pluginChainsFor(ctx)
	if chains == nil {
		return
	}
	for _, phase := range []struct {
		name  pluginapi.Kind
		chain *plugins.Chain
	}{{pluginapi.KindResponse, chains.Response}, {pluginapi.KindStream, chains.Stream}} {
		for _, inst := range phase.chain.Instances() {
			key := route.path + "|" + string(phase.name) + "|" + inst.Name
			if _, warned := skippedResponsePluginsWarned.LoadOrStore(key, struct{}{}); warned {
				continue
			}
			slog.Warn("guardrails of this phase do not run on decision answers, which are relayed unchanged; only the prompt phase applies",
				"path", route.path, "phase", phase.name, "guardrail", inst.Name, "type", inst.Type)
		}
	}
}

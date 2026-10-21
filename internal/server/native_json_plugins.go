package server

import (
	"context"
	"log/slog"
	"sync"

	"github.com/enterpilot/gomodel/internal/plugins"
)

// skippedResponsePluginsWarned records the route and guardrail pairs already
// warned about, so a guardrail scoped to every model is reported once per
// decision route rather than on every request.
var skippedResponsePluginsWarned sync.Map

// warnSkippedResponsePlugins logs the response and stream phase guardrails
// the workflow selects for a decision route, once per route and guardrail.
// They do not run there: a decision answer is typed values (probabilities,
// choices, scores), not a completion those phases inspect, so it is relayed
// unchanged. Without the warning, a response-phase guardrail scoped to every
// model would look as if it covered decision answers too.
func (s *translatedInferenceService) warnSkippedResponsePlugins(ctx context.Context, route nativeJSONRoute) {
	chains := s.pluginChainsFor(ctx)
	if chains == nil {
		return
	}
	for _, chain := range []*plugins.Chain{chains.Response, chains.Stream} {
		for _, inst := range chain.Instances() {
			if _, warned := skippedResponsePluginsWarned.LoadOrStore(route.path+"|"+inst.Name, struct{}{}); warned {
				continue
			}
			slog.Warn("response-phase guardrails do not run on decision answers, which are relayed unchanged; only the prompt phase applies",
				"path", route.path, "guardrail", inst.Name, "type", inst.Type)
		}
	}
}

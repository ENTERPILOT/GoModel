package app

import (
	"strings"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

// bindRewriterInference hands every inference-aware rewriter a handle on the
// gateway's internal executors, attributed to the rewriter's name. Like
// bindAuthenticationEventRecorders it runs after every fallible phase, so a
// failed replacement generation never rebinds the serving one's rewriters.
func bindRewriterInference(registry *ext.Registry, deps plugins.HostDeps) {
	if registry == nil {
		return
	}
	for _, rewriter := range registry.Rewriters() {
		if nilInterface(rewriter) {
			continue
		}
		if aware, ok := rewriter.(ext.InferenceAware); ok && !nilInterface(aware) {
			aware.SetInference(newExtensionInference(deps, userPathSegment(rewriter.Name())))
		}
	}
}

// userPathSegment makes an extension name usable as one user-path segment:
// '/' and ':' become '-', and an empty or dot-only name becomes "extension".
func userPathSegment(name string) string {
	segment := strings.NewReplacer("/", "-", ":", "-").Replace(strings.TrimSpace(name))
	if strings.Trim(segment, ".") == "" {
		return "extension"
	}
	return segment
}

// extensionInference adapts a plugin host scoped to extensions to
// ext.Inference, so extension calls share the plugin path: origin "plugin",
// no guardrails, usage and audit under the scoped user path.
type extensionInference struct {
	pluginapi.Inference
	pluginapi.Decisions
}

func newExtensionInference(deps plugins.HostDeps, name string) ext.Inference {
	host := plugins.NewHost(deps, plugins.HostInfo{PluginName: name, InstanceName: name, Scope: plugins.ScopeExtensions})
	return extensionInference{
		Inference: host.Inference(),
		Decisions: host.(pluginapi.DecisionsHost).Decisions(),
	}
}

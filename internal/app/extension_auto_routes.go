package app

import "github.com/enterpilot/gomodel/ext"

// bindRewriterAutoRoutes hands every rewriter that routes by level the lookup
// of virtual models using the "auto" strategy. Like bindRewriterInference it
// runs after every fallible phase, so a failed replacement generation never
// rebinds the serving one's rewriters.
func bindRewriterAutoRoutes(registry *ext.Registry, routes ext.AutoRoutes) {
	if registry == nil || nilInterface(routes) {
		return
	}
	for _, rewriter := range registry.Rewriters() {
		if nilInterface(rewriter) {
			continue
		}
		if aware, ok := rewriter.(ext.AutoRoutesAware); ok && !nilInterface(aware) {
			aware.SetAutoRoutes(routes)
		}
	}
}

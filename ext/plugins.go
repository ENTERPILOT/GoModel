package ext

import "github.com/enterpilot/gomodel/pluginapi"

// PluginFactory builds a fresh plugin value. Every configured instance of
// the plugin type gets its own value, so plugins can keep per-instance state.
type PluginFactory func() pluginapi.Plugin

// RegisterPlugin adds a compiled-in plugin type to the catalog. The
// manifest name becomes the guardrail type operators configure. Register
// before the server is constructed.
func (r *Registry) RegisterPlugin(factory PluginFactory) {
	if factory == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.plugins = append(r.plugins, factory)
}

// Plugins returns a defensive copy of the registered plugin factories.
func (r *Registry) Plugins() []PluginFactory {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]PluginFactory(nil), r.plugins...)
}

// RegisterPlugin registers a plugin factory on the Default registry.
func RegisterPlugin(factory PluginFactory) { Default.RegisterPlugin(factory) }

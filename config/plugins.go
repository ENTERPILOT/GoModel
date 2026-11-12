package config

import "log/slog"

// PluginsConfig controls the plugin system and the loading of plugin shared
// objects (.so files).
//
// A shared object is trusted code: loading one is equivalent to changing the
// binary. Keep search_paths root-owned and pin files with sha256 in
// production. Loading requires a cgo-enabled build of GoModel on Linux,
// macOS, or FreeBSD (the gomodel:<version>-plugins image); the default static
// binary reports a clear error for any configured plugin file.
type PluginsConfig struct {
	// Enabled turns the plugin system on: the built-in and loaded plugin
	// types, the guardrails built from them, the routing-strategy plugins
	// virtual models can select, and the /admin/plugins and /admin/guardrails
	// endpoints. Guardrails need plugins, so GUARDRAILS_ENABLED=true enables
	// it implicitly.
	// Default: false
	Enabled bool `yaml:"enabled" env:"PLUGINS_ENABLED"`

	// SearchPaths lists directories searched for relative plugin files.
	// A relative file must resolve inside one of these directories; the first
	// match wins. Absolute file paths are used as-is.
	// Default: empty (no .so loading)
	SearchPaths []string `yaml:"search_paths" env:"PLUGINS_SEARCH_PATHS"`

	// RequireSHA256 refuses to start when a Load entry has no sha256 pin, so
	// a shared object replaced on disk cannot run as the gateway. Turn it on
	// in production.
	// Default: false
	RequireSHA256 bool `yaml:"require_sha256" env:"PLUGINS_REQUIRE_SHA256"`

	// Load lists the shared objects to open at startup. Each file exports a
	// GoModelPlugin symbol (see the pluginapi package). A file that cannot be
	// resolved, verified, or opened is a startup error.
	// Env: PLUGINS_LOAD is a comma-separated list of file names, each
	// optionally "file=sha256hex" to pin the digest, and REPLACES this list
	// when set (env > config), like every other env-configurable value. It
	// exists so an install can load plugins without a config.yaml.
	Load []PluginFileConfig `yaml:"load"`
}

// applyPluginDependencies enables the plugin system when guardrails are
// enabled, since every guardrail is a plugin instance, and warns when files
// are listed for loading while the system is off.
func applyPluginDependencies(cfg *Config) {
	if cfg == nil {
		return
	}
	if cfg.Guardrails.Enabled && !cfg.Plugins.Enabled {
		cfg.Plugins.Enabled = true
		slog.Info("plugin system enabled because guardrails are enabled",
			"guardrails_enabled", true,
			"plugins_enabled", true,
		)
	}
	if !cfg.Plugins.Enabled && len(cfg.Plugins.Load) > 0 {
		slog.Warn("plugins.load ignored because the plugin system is disabled",
			"files", len(cfg.Plugins.Load),
			"hint", "set PLUGINS_ENABLED=true to load them",
		)
	}
}

// PluginFileConfig identifies one shared object to load.
type PluginFileConfig struct {
	// File is the .so path: absolute, or relative to one of SearchPaths.
	File string `yaml:"file"`

	// SHA256 is a hex digest of the file. When set, a mismatch fails
	// startup. Optional unless PluginsConfig.RequireSHA256 is set.
	SHA256 string `yaml:"sha256"`
}

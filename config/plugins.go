package config

// PluginsConfig controls loading of plugin shared objects (.so files).
//
// A shared object is trusted code: loading one is equivalent to changing the
// binary. Keep search_paths root-owned and pin files with sha256 in
// production. Loading requires a cgo-enabled build of GoModel on Linux,
// macOS, or FreeBSD (the gomodel:<version>-plugins image); the default static
// binary reports a clear error for any configured plugin file.
type PluginsConfig struct {
	// SearchPaths lists directories searched for relative plugin files.
	// A relative file must resolve inside one of these directories; the first
	// match wins. Absolute file paths are used as-is.
	// Default: empty (no .so loading)
	SearchPaths []string `yaml:"search_paths" env:"PLUGINS_SEARCH_PATHS"`

	// Load lists the shared objects to open at startup. Each file exports a
	// GoModelPlugin symbol (see the pluginapi package). A file that cannot be
	// resolved, verified, or opened is a startup error.
	Load []PluginFileConfig `yaml:"load"`
}

// PluginFileConfig identifies one shared object to load.
type PluginFileConfig struct {
	// File is the .so path: absolute, or relative to one of SearchPaths.
	File string `yaml:"file"`

	// SHA256 is an optional hex digest of the file. When set, a mismatch
	// fails startup.
	SHA256 string `yaml:"sha256"`
}

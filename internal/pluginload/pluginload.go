// Package pluginload opens plugin shared objects (.so) built against the
// pluginapi contract.
//
// A shared object is trusted code: opening one is equivalent to changing the
// binary. The loader therefore refuses relative paths that escape the
// configured search paths, verifies optional SHA-256 pins, and turns every
// failure into a startup error that names the file. It never loads anything
// unless plugins.load is configured.
//
// Go's plugin package requires the host and the plugin to be built with the
// same toolchain and the same build flags (notably -trimpath and -race) from
// identical sources of every shared package. [HostBuildInfo] and
// [HostBuildFlags] expose what this binary was built with so tooling can
// reproduce it.
package pluginload

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/pluginapi"
)

// HostBuildInfo describes the toolchain this binary was built with. Plugins
// must be built with the same Go version against the same pluginapi sources.
var HostBuildInfo = pluginapi.BuildInfo{
	GoVersion:        runtime.Version(),
	PluginAPIVersion: pluginapi.Version,
}

// Loaded is one opened shared object.
type Loaded struct {
	// Path is the resolved absolute path of the shared object.
	Path string
	// Factory returns a plugin instance. For a constructor symbol every call
	// returns a fresh instance; for a variable symbol every call returns the
	// same value (see SingleInstance).
	Factory func() pluginapi.Plugin
	// Manifest is the manifest reported by a probe instance, with BuiltWith
	// filled from BuildInfo when the plugin left it empty.
	Manifest pluginapi.Manifest
	// BuildInfo is the GoModelBuildInfo symbol stamped by `gomodel plugin
	// build`, or the zero value when the plugin does not export one.
	BuildInfo pluginapi.BuildInfo
	// SingleInstance reports that GoModelPlugin is a variable rather than a
	// constructor, so the shared object can back only one configured
	// instance.
	SingleInstance bool
}

// Load resolves, verifies, and opens every configured shared object. It
// returns nil when nothing is configured. The first failure aborts loading
// with an error naming the offending file.
func Load(cfg config.PluginsConfig) ([]Loaded, error) {
	if len(cfg.Load) == 0 {
		return nil, nil
	}
	loaded := make([]Loaded, 0, len(cfg.Load))
	for i, entry := range cfg.Load {
		path, err := Resolve(entry.File, cfg.SearchPaths)
		if err != nil {
			return nil, fmt.Errorf("plugins.load[%d]: %w", i, err)
		}
		if err := VerifySHA256(path, entry.SHA256); err != nil {
			return nil, fmt.Errorf("plugins.load[%d]: %w", i, err)
		}
		l, err := Open(path)
		if err != nil {
			return nil, fmt.Errorf("plugins.load[%d]: %w", i, err)
		}
		loaded = append(loaded, l)
	}
	return loaded, nil
}

// Resolve turns a configured plugin file into an absolute path. Absolute
// files are used as-is. Relative files are looked up in searchPaths in order
// and must stay inside the directory they are found in, symlinks included.
func Resolve(file string, searchPaths []string) (string, error) {
	file = strings.TrimSpace(file)
	if file == "" {
		return "", errors.New("plugin file is empty")
	}
	if filepath.IsAbs(file) {
		path := filepath.Clean(file)
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("plugin file %s: %w", path, err)
		}
		return path, nil
	}
	if len(searchPaths) == 0 {
		return "", fmt.Errorf("plugin file %q is relative but plugins.search_paths is empty (use an absolute path or configure search_paths)", file)
	}
	for _, dir := range searchPaths {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		absDir, err := filepath.Abs(dir)
		if err != nil {
			return "", fmt.Errorf("plugins.search_paths entry %q: %w", dir, err)
		}
		candidate := filepath.Join(absDir, file)
		if !within(absDir, candidate) {
			return "", fmt.Errorf("plugin file %q escapes search path %s", file, absDir)
		}
		if _, err := os.Stat(candidate); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", fmt.Errorf("plugin file %s: %w", candidate, err)
		}
		realDir, err := filepath.EvalSymlinks(absDir)
		if err != nil {
			return "", fmt.Errorf("plugins.search_paths entry %s: %w", absDir, err)
		}
		realPath, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			return "", fmt.Errorf("plugin file %s: %w", candidate, err)
		}
		if !within(realDir, realPath) {
			return "", fmt.Errorf("plugin file %q resolves to %s, outside search path %s", file, realPath, realDir)
		}
		return realPath, nil
	}
	return "", fmt.Errorf("plugin file %q not found in plugins.search_paths %v", file, searchPaths)
}

// within reports whether path is dir or lies below it. Both must be clean
// absolute paths.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// VerifySHA256 checks the file against a hex digest. An empty digest skips
// the check.
func VerifySHA256(path, want string) error {
	want = strings.ToLower(strings.TrimSpace(want))
	want = strings.TrimPrefix(want, "sha256:")
	if want == "" {
		return nil
	}
	if len(want) != sha256.Size*2 {
		return fmt.Errorf("plugin file %s: sha256 %q is not a 64-character hex digest", path, want)
	}
	if _, err := hex.DecodeString(want); err != nil {
		return fmt.Errorf("plugin file %s: sha256 %q is not hex: %w", path, want, err)
	}
	got, err := FileSHA256(path)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("plugin file %s: sha256 mismatch: file is %s, config expects %s", path, got, want)
	}
	return nil
}

// FileSHA256 returns the lowercase hex SHA-256 digest of the file.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("plugin file %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("plugin file %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

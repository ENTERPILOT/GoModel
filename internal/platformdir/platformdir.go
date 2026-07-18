// Package platformdir resolves the OS-conventional per-user directories for
// GoModel's durable data and caches, used when no explicit path is
// configured. Binary installs (install.sh / Homebrew / install.ps1) run from
// arbitrary working directories, so CWD-relative defaults would scatter
// state; these follow each platform's convention instead.
package platformdir

import (
	"os"
	"path/filepath"
	"runtime"
)

const app = "gomodel"

// DataDir returns the directory for durable application data such as the
// SQLite database:
//
//	Linux    $XDG_DATA_HOME/gomodel (default ~/.local/share/gomodel)
//	macOS    ~/Library/Application Support/gomodel
//	Windows  %LocalAppData%\gomodel
func DataDir() (string, error) {
	switch runtime.GOOS {
	case "windows":
		base, err := os.UserCacheDir() // %LocalAppData%
		if err != nil {
			return "", err
		}
		return filepath.Join(base, app), nil
	case "darwin":
		base, err := os.UserConfigDir() // ~/Library/Application Support
		if err != nil {
			return "", err
		}
		return filepath.Join(base, app), nil
	default:
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			return filepath.Join(xdg, app), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "share", app), nil
	}
}

// CacheDir returns the directory for re-creatable caches such as the model
// catalog:
//
//	Linux    $XDG_CACHE_HOME/gomodel (default ~/.cache/gomodel)
//	macOS    ~/Library/Caches/gomodel
//	Windows  %LocalAppData%\gomodel\cache
func CacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		// UserCacheDir is %LocalAppData%, shared with DataDir: keep the
		// cache in a subdirectory so the two stay distinguishable.
		return filepath.Join(base, app, "cache"), nil
	}
	return filepath.Join(base, app), nil
}

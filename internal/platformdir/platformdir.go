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

// LocalDataDir is the project-local data directory. Deployments that already
// have one — the Docker image, anyone running from a checkout — keep their
// state there, so upgrades never move a database or a pid file out from under
// an operator.
const LocalDataDir = "data"

// DataFile returns where a durable data file called name belongs: inside
// LocalDataDir when that directory exists next to the process, otherwise inside
// DataDir. Callers share this rule so a deployment's files stay together
// instead of some landing project-local and others in the per-user directory.
//
// The local form is spelled with a forward slash on every platform, which
// Windows accepts, so it stays comparable to the legacy path constants built
// the same way.
func DataFile(name string) string {
	local := LocalDataDir + "/" + name
	if info, err := os.Stat(LocalDataDir); err == nil && info.IsDir() {
		return local
	}
	dir, err := DataDir()
	if err != nil {
		return local
	}
	return filepath.Join(dir, name)
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

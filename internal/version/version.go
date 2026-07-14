package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// These variables are set via -ldflags during the build process.
// When ldflags are absent (e.g. go install), init fills them from
// the embedded module build info instead.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

func init() {
	if Version != "dev" {
		return
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		Version = v
	}
	for _, s := range bi.Settings {
		if s.Value == "" {
			continue
		}
		switch s.Key {
		case "vcs.revision":
			Commit = s.Value
		case "vcs.time":
			Date = s.Value
		}
	}
}

// Info returns a formatted version string
func Info() string {
	return fmt.Sprintf("gomodel %s (commit: %s, built: %s, %s)", Version, Commit, Date, runtime.Version())
}

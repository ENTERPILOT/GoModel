package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
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

// Distribution names carried in the X-GoModel-App header and used to pick
// which version manifest an update check reads.
const (
	AppCore = "GoModel"
	AppPro  = "GoModel Pro"
)

// App names the running distribution. Custom builds set it through
// run.Options.AppName (or -ldflags) before the gateway starts; the open
// core leaves it at AppCore.
var App = AppCore

// Channel is the manifest basename for the running distribution.
func Channel() string { return ChannelFor(App) }

// ChannelFor is the manifest basename for a distribution name: "pro" for
// GoModel Pro, "core" for everything else. It is the single place that rule
// lives, so the update check and the version banner can never disagree.
func ChannelFor(app string) string {
	if strings.EqualFold(strings.TrimSpace(app), AppPro) {
		return "pro"
	}
	return "core"
}

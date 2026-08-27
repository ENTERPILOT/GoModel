package app

import (
	"log/slog"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/version"
	"github.com/enterpilot/gomodel/internal/versioncheck"
)

// newVersionChecker builds the update checker from configuration. The install
// identifier is only materialized when checks are enabled, so a deployment
// that opted out never writes one.
func newVersionChecker(cfg config.VersionCheckConfig) *versioncheck.Checker {
	checkerCfg := versioncheck.Config{
		Enabled:        cfg.Enabled,
		URL:            cfg.URL,
		App:            version.App,
		Version:        version.Version,
		Interval:       time.Duration(cfg.IntervalHours) * time.Hour,
		Timeout:        time.Duration(cfg.TimeoutSeconds) * time.Second,
		MaxDailyChecks: cfg.MaxDailyChecks,
	}
	if cfg.Enabled {
		checkerCfg.InstallID = versioncheck.InstallID()
		if versioncheck.LeaksQueryInCleartext(cfg.URL) {
			slog.Warn("version check URL sends its query string unencrypted; use https if it carries a credential",
				"host", versioncheck.SafeURL(cfg.URL))
		}
	}
	return versioncheck.New(checkerCfg)
}

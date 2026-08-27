package app

import (
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
	}
	return versioncheck.New(checkerCfg)
}

package app

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/platformdir"
	"github.com/enterpilot/gomodel/internal/runtimesettings"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/version"
	"github.com/enterpilot/gomodel/internal/versioncheck"
)

// newVersionChecker builds the update checker from configuration. The install
// identifier is only materialized when checks are enabled, so a deployment
// that opted out never writes one.
//
// The identifier is kept in the deployment's database first and the data
// directory second (see versioncheck.Identity), with the master key as the
// fallback that survives a container recreated without either.
func newVersionChecker(ctx context.Context, cfg config.VersionCheckConfig, backend storage.Storage, masterKey string) *versioncheck.Checker {
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
		var store versioncheck.Store
		if backend != nil {
			s, err := runtimesettings.NewStore(ctx, backend)
			if err != nil {
				slog.Warn("install id will be kept in the data directory only", "error", err)
			} else {
				store = s
			}
		}
		identity := versioncheck.NewIdentity(store, masterKey)
		id, source := identity.Resolve(ctx)
		if source == versioncheck.SourceGenerated || source == versioncheck.SourceDerived {
			slog.Info("install id created", "source", string(source))
		}
		// Resolved again per check rather than fixed here: if the database
		// was unreachable just now, a later check picks up the id it holds.
		checkerCfg.InstallID = id
		checkerCfg.InstallIDFunc = identity.ID
		if versioncheck.LeaksQueryInCleartext(cfg.URL) {
			slog.Warn("version check URL sends its query string unencrypted; use https if it carries a credential",
				"host", versioncheck.SafeURL(cfg.URL))
		}
	}
	return versioncheck.New(checkerCfg)
}

// warnIfDataDirEphemeral tells an operator whose SQLite database sits on a
// container's own writable layer that it will not survive the container.
// Only SQLite is checked: with PostgreSQL or MongoDB the data directory holds
// nothing that is not also in the database.
func warnIfDataDirEphemeral(cfg storage.Config) {
	if cfg.Type != storage.TypeSQLite {
		return
	}
	path := cfg.SQLite.Path
	if path == "" {
		path = storage.DefaultSQLitePath()
	}
	dir := filepath.Dir(path)
	if platformdir.Ephemeral(dir) {
		slog.Warn("data directory is on the container's own filesystem, not a volume: "+
			"the database (audit logs, usage, budgets, keys) and the install identity are lost when the container is recreated; "+
			"mount a volume there or use PostgreSQL/MongoDB",
			"dir", dir)
	}
}

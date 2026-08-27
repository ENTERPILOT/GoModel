package config

import "github.com/enterpilot/gomodel/internal/versioncheck"

// VersionCheckConfig controls the daily update check against the GoModel
// release manifest.
//
// The check sends the running version, the distribution name, and an
// anonymous install identifier. It never sends API keys, provider
// credentials, model names, prompts, usage data, client addresses, or the
// hostname the gateway is served on. Set Enabled to false to stop all
// outbound traffic from this subsystem.
type VersionCheckConfig struct {
	// Enabled turns the daily update check on.
	// Default: true
	Enabled bool `yaml:"enabled" env:"GOMODEL_VERSION_CHECK_ENABLED"`

	// URL is the base URL of the version manifest. The channel file
	// ("core.txt" or "pro.txt") is appended to it.
	// Default: https://gomodel.enterpilot.io/version
	URL string `yaml:"url" env:"GOMODEL_VERSION_CHECK_URL"`

	// IntervalHours is how often the background check runs. Each run is
	// jittered so gateways started together do not query in lockstep.
	// Default: 24
	IntervalHours int `yaml:"interval_hours" env:"GOMODEL_VERSION_CHECK_INTERVAL_HOURS"`

	// TimeoutSeconds bounds a single manifest request.
	// Default: 5
	TimeoutSeconds int `yaml:"timeout_seconds" env:"GOMODEL_VERSION_CHECK_TIMEOUT_SECONDS"`

	// MaxDailyChecks caps how many manifest requests this gateway makes per
	// day in total, so a hostile client cycling cookies cannot turn /version
	// into an outbound request amplifier.
	// Default: 500
	MaxDailyChecks int `yaml:"max_daily_checks" env:"GOMODEL_VERSION_CHECK_MAX_DAILY"`
}

// DefaultVersionCheckURL is the public release manifest served by the GoModel
// website. "/core.txt" or "/pro.txt" is appended per distribution.
const DefaultVersionCheckURL = versioncheck.DefaultURL

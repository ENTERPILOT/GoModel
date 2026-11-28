package config

import "strings"

// AdminConfig holds configuration for the admin API and dashboard UI.
type AdminConfig struct {
	// EndpointsEnabled controls whether the admin REST API is active
	// Default: true
	EndpointsEnabled bool `yaml:"endpoints_enabled" env:"ADMIN_ENDPOINTS_ENABLED"`

	// UIEnabled controls whether the admin dashboard UI is active
	// Requires EndpointsEnabled — if endpoints are disabled and UI is enabled,
	// a warning is logged and UI is forced to false.
	// Default: true
	UIEnabled bool `yaml:"ui_enabled" env:"ADMIN_UI_ENABLED"`

	// Username is the name the dashboard login accepts together with Password.
	// Default: admin
	Username string `yaml:"username" env:"DASHBOARD_USERNAME"`

	// Password turns on the dashboard's username and password login. Signing
	// in starts a browser session that reaches the dashboard and the admin
	// API without the master key, and the admin API then always requires a
	// credential, even with no master key configured.
	// Default: empty (no login; the dashboard asks for an API key)
	Password string `yaml:"password" env:"DASHBOARD_PASSWORD" export:"secret"`

	// LiveLogsEnabled controls whether the dashboard opens a realtime log stream.
	// Default: true
	LiveLogsEnabled bool `yaml:"live_logs_enabled" env:"DASHBOARD_LIVE_LOGS_ENABLED"`

	// LiveLogsBufferSize is the in-memory replay window for dashboard live log events.
	// Default: 10000
	LiveLogsBufferSize int `yaml:"live_logs_buffer_size" env:"DASHBOARD_LIVE_LOGS_BUFFER_SIZE"`

	// LiveLogsReplayLimit caps events replayed to one reconnecting dashboard client.
	// Default: 1000
	LiveLogsReplayLimit int `yaml:"live_logs_replay_limit" env:"DASHBOARD_LIVE_LOGS_REPLAY_LIMIT"`

	// LiveLogsHeartbeatSeconds keeps idle stream connections and proxies active.
	// Default: 15
	LiveLogsHeartbeatSeconds int `yaml:"live_logs_heartbeat_seconds" env:"DASHBOARD_LIVE_LOGS_HEARTBEAT_SECONDS"`
}

// DefaultAdminUsername is the dashboard login's username when none is set.
const DefaultAdminUsername = "admin"

// LoginEnabled reports whether the dashboard's username and password login is
// configured. It guards the admin API, so it needs the admin endpoints.
func (c AdminConfig) LoginEnabled() bool {
	return c.EndpointsEnabled && c.Password != ""
}

// normalizeAdminLogin trims the dashboard login's username and falls back to
// the default for a blank one, so a password alone is enough to turn the login
// on. The password is used exactly as written.
func normalizeAdminLogin(cfg *AdminConfig) {
	cfg.Username = strings.TrimSpace(cfg.Username)
	if cfg.Username == "" {
		cfg.Username = DefaultAdminUsername
	}
}

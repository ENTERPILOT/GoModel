package config

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

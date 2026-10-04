package config

import "github.com/enterpilot/gomodel/internal/storage"

// StorageConfig holds database storage configuration (used by audit logging, usage tracking, future IAM, etc.)
type StorageConfig struct {
	// Type specifies the storage backend: "sqlite" (default), "postgresql", or "mongodb"
	Type string `yaml:"type" env:"STORAGE_TYPE"`

	// SQLite configuration
	SQLite SQLiteStorageConfig `yaml:"sqlite"`

	// PostgreSQL configuration
	PostgreSQL PostgreSQLStorageConfig `yaml:"postgresql"`

	// MongoDB configuration
	MongoDB MongoDBStorageConfig `yaml:"mongodb"`

	// EncryptionKey enables encryption at rest for dashboard-managed secrets
	// (provider credentials, MCP headers, guardrail secrets). Any string is
	// accepted; 32 random bytes in base64 (openssl rand -base64 32) is
	// recommended. Losing it makes those secrets unrecoverable.
	// Default: empty (secrets are stored in plaintext).
	EncryptionKey string `yaml:"encryption_key" env:"GOMODEL_ENCRYPTION_KEY"`

	// EncryptionKeyPrevious is the old EncryptionKey during a key rotation.
	// It is only used at startup to re-wrap the data key with EncryptionKey.
	EncryptionKeyPrevious string `yaml:"encryption_key_previous" env:"GOMODEL_ENCRYPTION_KEY_PREVIOUS"`
}

// SQLiteStorageConfig holds SQLite-specific storage configuration
type SQLiteStorageConfig struct {
	// Path is the database file path. Default: ./data/gomodel.db when a
	// ./data directory exists, otherwise the OS per-user data directory
	// (e.g. ~/.local/share/gomodel/gomodel.db).
	Path string `yaml:"path" env:"SQLITE_PATH"`
}

// PostgreSQLStorageConfig holds PostgreSQL-specific storage configuration
type PostgreSQLStorageConfig struct {
	// URL is the connection string (e.g., postgres://user:pass@localhost/dbname)
	URL string `yaml:"url" env:"POSTGRES_URL"`
	// MaxConns is the maximum connection pool size (default: 10)
	MaxConns int `yaml:"max_conns" env:"POSTGRES_MAX_CONNS"`
}

// MongoDBStorageConfig holds MongoDB-specific storage configuration
type MongoDBStorageConfig struct {
	// URL is the connection string; a database named in its path is honored
	// (e.g., mongodb://localhost:27017/gomodel)
	URL string `yaml:"url" env:"MONGODB_URL"`
	// Database overrides the database named in the URL (default: gomodel)
	Database string `yaml:"database" env:"MONGODB_DATABASE"`
}

// BackendConfig converts the application storage config into the internal storage config.
func (c StorageConfig) BackendConfig() storage.Config {
	cfg := storage.Config{
		Type: c.Type,
		SQLite: storage.SQLiteConfig{
			Path: c.SQLite.Path,
		},
		PostgreSQL: storage.PostgreSQLConfig{
			URL:      c.PostgreSQL.URL,
			MaxConns: c.PostgreSQL.MaxConns,
		},
		MongoDB: storage.MongoDBConfig{
			URL:      c.MongoDB.URL,
			Database: c.MongoDB.Database,
		},
	}
	if cfg.Type == "" {
		cfg.Type = storage.TypeSQLite
	}
	if cfg.SQLite.Path == "" {
		cfg.SQLite.Path = storage.DefaultSQLitePath()
	}
	return cfg
}

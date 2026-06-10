package config

import "gomodel/internal/storage"

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
}

// SQLiteStorageConfig holds SQLite-specific storage configuration
type SQLiteStorageConfig struct {
	// Path is the database file path (default: data/gomodel.db)
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
		cfg.SQLite.Path = storage.DefaultSQLitePath
	}
	return cfg
}

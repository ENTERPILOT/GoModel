// Package storage provides shared database connections for all features.
// This abstraction allows multiple features (audit logging, IAM, guardrails)
// to share a single database connection.
package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// Type constants for storage backends
const (
	TypeSQLite     = "sqlite"
	TypePostgreSQL = "postgresql"
	TypeMongoDB    = "mongodb"
)

// DefaultSQLitePath is the default file path for the SQLite database.
const DefaultSQLitePath = "data/gomodel.db"

// Config holds storage configuration
type Config struct {
	// Type specifies the storage backend: "sqlite", "postgresql", or "mongodb"
	Type string

	// SQLite configuration
	SQLite SQLiteConfig

	// PostgreSQL configuration
	PostgreSQL PostgreSQLConfig

	// MongoDB configuration
	MongoDB MongoDBConfig
}

// SQLiteConfig holds SQLite-specific configuration
type SQLiteConfig struct {
	// Path is the database file path (default: data/gomodel.db)
	Path string
}

// PostgreSQLConfig holds PostgreSQL-specific configuration
type PostgreSQLConfig struct {
	// URL is the connection string (e.g., postgres://user:pass@localhost/dbname)
	URL string
	// MaxConns is the maximum connection pool size (default: 10)
	MaxConns int
}

// MongoDBConfig holds MongoDB-specific configuration
type MongoDBConfig struct {
	// URL is the connection string (e.g., mongodb://localhost:27017)
	URL string
	// Database is the database name (default: gomodel)
	Database string
}

// Storage provides a unified interface for database connections.
// Implementations must be safe for concurrent use.
type Storage interface {
	// Type returns the storage type ("sqlite", "postgresql", or "mongodb")
	Type() string

	// SQLiteDB returns the *sql.DB connection for SQLite.
	// Returns nil if not using SQLite.
	SQLiteDB() *sql.DB

	// PostgreSQLPool returns the connection pool for PostgreSQL.
	// Returns nil if not using PostgreSQL.
	// The actual type is *pgxpool.Pool but we use interface{} to avoid import cycles.
	PostgreSQLPool() any

	// MongoDatabase returns the MongoDB database.
	// Returns nil if not using MongoDB.
	// The actual type is *mongo.Database but we use interface{} to avoid import cycles.
	MongoDatabase() any

	// Close releases all resources held by the storage.
	Close() error
}

// New creates a new Storage based on the configuration.
// It validates the configuration and establishes the database connection.
func New(ctx context.Context, cfg Config) (Storage, error) {
	switch cfg.Type {
	case TypeSQLite:
		return NewSQLite(cfg.SQLite)
	case TypePostgreSQL:
		return NewPostgreSQL(ctx, cfg.PostgreSQL)
	case TypeMongoDB:
		return NewMongoDB(ctx, cfg.MongoDB)
	default:
		return nil, fmt.Errorf("unknown storage type: %s (valid: sqlite, postgresql, mongodb)", cfg.Type)
	}
}

// DefaultConfig returns a Config with sensible defaults
func DefaultConfig() Config {
	return Config{
		Type: TypeSQLite,
		SQLite: SQLiteConfig{
			Path: DefaultSQLitePath,
		},
		PostgreSQL: PostgreSQLConfig{
			MaxConns: 10,
		},
		MongoDB: MongoDBConfig{
			Database: "gomodel",
		},
	}
}

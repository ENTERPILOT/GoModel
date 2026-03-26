// Package storage provides shared database connections for all features.
// This abstraction allows multiple features (audit logging, IAM, guardrails)
// to share a single database connection.
package storage

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.mongodb.org/mongo-driver/v2/mongo"
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

// Storage manages the lifecycle of a shared storage backend.
type Storage interface {
	Close() error
}

// SQLiteStorage exposes a SQLite database handle.
type SQLiteStorage interface {
	Storage
	DB() *sql.DB
}

// PostgreSQLStorage exposes a PostgreSQL connection pool.
type PostgreSQLStorage interface {
	Storage
	Pool() *pgxpool.Pool
}

// MongoDBStorage exposes a MongoDB database handle.
type MongoDBStorage interface {
	Storage
	Database() *mongo.Database
}

// ResolveBackend dispatches to the callback matching the concrete storage backend.
func ResolveBackend[T any](
	store Storage,
	sqlite func(*sql.DB) (T, error),
	postgresql func(*pgxpool.Pool) (T, error),
	mongodb func(*mongo.Database) (T, error),
) (T, error) {
	var zero T

	switch store := store.(type) {
	case SQLiteStorage:
		if sqlite == nil {
			return zero, fmt.Errorf("sqlite handler is nil")
		}
		return sqlite(store.DB())
	case PostgreSQLStorage:
		if postgresql == nil {
			return zero, fmt.Errorf("postgresql handler is nil")
		}
		return postgresql(store.Pool())
	case MongoDBStorage:
		if mongodb == nil {
			return zero, fmt.Errorf("mongodb handler is nil")
		}
		return mongodb(store.Database())
	default:
		return zero, fmt.Errorf("unsupported storage backend %T", store)
	}
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

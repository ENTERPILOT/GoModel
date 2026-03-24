package storage

import (
	"context"
	"database/sql"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5/pgxpool"
)

// postgresStorage implements Storage for PostgreSQL
type postgresStorage struct {
	pool *pgxpool.Pool
}

// NewPostgreSQL creates a new PostgreSQL storage connection.
// It creates a connection pool for efficient connection reuse.
func NewPostgreSQL(ctx context.Context, cfg PostgreSQLConfig) (Storage, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("PostgreSQL URL is required")
	}

	// Parse the connection string and create pool config
	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse PostgreSQL URL: %w", err)
	}

	// Set connection pool size
	if cfg.MaxConns > 0 {
		maxConns := min(cfg.MaxConns, math.MaxInt32)
		poolCfg.MaxConns = int32(maxConns)
	} else {
		poolCfg.MaxConns = 10 // default
	}

	// Create the connection pool
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create PostgreSQL connection pool: %w", err)
	}

	// Verify connection
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ping PostgreSQL: %w", err)
	}

	return &postgresStorage{pool: pool}, nil
}

func (s *postgresStorage) Type() string {
	return TypePostgreSQL
}

func (s *postgresStorage) SQLiteDB() *sql.DB {
	return nil
}

func (s *postgresStorage) PostgreSQLPool() any {
	return s.pool
}

func (s *postgresStorage) MongoDatabase() any {
	return nil
}

func (s *postgresStorage) Close() error {
	if s.pool != nil {
		s.pool.Close()
	}
	return nil
}

// Pool returns the underlying pgxpool.Pool for direct access
func (s *postgresStorage) Pool() *pgxpool.Pool {
	return s.pool
}

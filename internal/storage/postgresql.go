package storage

import (
	"context"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/enterpilot/gomodel/egress"
)

// postgresStorage implements Storage for PostgreSQL
type postgresStorage struct {
	pool *pgxpool.Pool
}

// NewPostgreSQL creates a new PostgreSQL storage connection.
// It creates a connection pool for efficient connection reuse.
func NewPostgreSQL(ctx context.Context, cfg PostgreSQLConfig) (PostgreSQLStorage, error) {
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

	// Every connection the pool opens goes through the egress hook, so a
	// distribution that polices outbound traffic sees the database too. pgx
	// resolves the host before it dials, so the lookup is deferred to the
	// hook as well: with one installed the name reaches it unresolved and
	// the hook dials the address that passed, and without one pgx's own
	// resolver answers. Both are decided per connection, so a hook installed
	// after this pool was built still sees hostnames.
	poolCfg.ConnConfig.DialFunc = egress.DialContext
	resolve := poolCfg.ConnConfig.LookupFunc
	poolCfg.ConnConfig.LookupFunc = func(ctx context.Context, host string) ([]string, error) {
		return egress.Lookup(ctx, host, resolve)
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

// Ping verifies connectivity to PostgreSQL.
func (s *postgresStorage) Ping(ctx context.Context) error {
	if s.pool == nil {
		return fmt.Errorf("postgresql pool is not initialized")
	}
	return s.pool.Ping(ctx)
}

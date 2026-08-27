// Package sqlxtest runs a store's test suite against every SQL dialect.
//
// Before the sqlx adapter existed, each store was written twice and only the
// SQLite half was tested: 18 of 22 PostgreSQL store implementations had no
// test that touched a database. A single implementation behind sqlx.DB means
// one suite can cover both backends, so store tests should call Run rather
// than opening a *sql.DB directly.
//
// SQLite always runs, in memory. PostgreSQL runs only when
// GOMODEL_TEST_POSTGRES_URL names a reachable server; otherwise that subtest
// skips. The variable is deliberately not POSTGRES_URL — pointing a suite that
// creates and drops schemas at a configured application database should take a
// separate, explicit opt-in.
package sqlxtest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "modernc.org/sqlite" // SQLite driver for the in-memory test database

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// PostgresURLEnv names the environment variable holding the test PostgreSQL
// connection string.
const PostgresURLEnv = "GOMODEL_TEST_POSTGRES_URL"

// schemaCounter keeps concurrently running PostgreSQL subtests in separate
// schemas. Tests must not depend on wall-clock time or randomness for naming.
var schemaCounter atomic.Uint64

// Run executes fn once per available dialect, as a subtest named after it.
// Each run receives an empty database that is discarded afterwards.
func Run(t *testing.T, fn func(t *testing.T, db sqlx.DB)) {
	t.Helper()

	t.Run(string(sqlx.SQLite), func(t *testing.T) {
		fn(t, NewSQLite(t))
	})

	t.Run(string(sqlx.PostgreSQL), func(t *testing.T) {
		db := newPostgres(t)
		if db == nil {
			return // newPostgres already skipped
		}
		fn(t, db)
	})
}

// NewSQLite returns an empty in-memory SQLite database for one test.
func NewSQLite(t *testing.T) sqlx.DB {
	t.Helper()

	// A shared cache with a per-test name keeps every pooled connection on the
	// same in-memory database; a plain ":memory:" DSN would give each pooled
	// connection its own empty one.
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", sanitizeIdentifier(t.Name()))
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	db, err := sqlx.NewSQLite(raw)
	if err != nil {
		t.Fatalf("wrap sqlite: %v", err)
	}
	return db
}

// newPostgres returns an empty PostgreSQL database scoped to a throwaway
// schema, or nil after skipping when no test server is configured.
func newPostgres(t *testing.T) sqlx.DB {
	t.Helper()

	pool := NewPostgresPool(t)
	if pool == nil {
		return nil // NewPostgresPool already skipped
	}
	db, err := sqlx.NewPostgreSQL(pool)
	if err != nil {
		t.Fatalf("wrap postgres: %v", err)
	}
	return db
}

// NewPostgresPool returns a connection pool scoped to a throwaway schema, or
// nil after skipping when no test server is configured.
//
// Run is the right entry point for a store written against sqlx.DB. This is
// for the few tests that also need the raw pool — a store that has not moved
// onto sqlx yet takes *pgxpool.Pool directly, and a test spanning both wants
// them pointed at one schema.
func NewPostgresPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	baseURL := strings.TrimSpace(os.Getenv(PostgresURLEnv))
	if baseURL == "" {
		t.Skipf("%s not set", PostgresURLEnv)
		return nil
	}

	ctx := context.Background()
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Skipf("connect to %s: %v", PostgresURLEnv, err)
		return nil
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Skipf("ping %s: %v", PostgresURLEnv, err)
		return nil
	}

	schema := fmt.Sprintf("sqlxtest_%s_%d", sanitizeIdentifier(t.Name()), schemaCounter.Add(1))
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+quoteIdentifier(schema)); err != nil {
		admin.Close()
		t.Fatalf("create schema %s: %v", schema, err)
	}
	t.Cleanup(func() {
		if err := dropTestSchema(context.Background(), admin, schema); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
		admin.Close()
	})

	// Pin every pooled connection to the throwaway schema so unqualified table
	// names in store SQL resolve there.
	cfg, err := pgxpool.ParseConfig(baseURL)
	if err != nil {
		t.Fatalf("parse %s: %v", PostgresURLEnv, err)
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("open scoped pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// sanitizeIdentifier reduces a test name to characters safe in a schema name
// or SQLite DSN, and bounds its length so the result stays inside
// PostgreSQL's 63-byte identifier limit once prefixed and numbered.
func sanitizeIdentifier(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// quoteIdentifier wraps an identifier in double quotes, escaping any it holds.
func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// dropTestSchema drops a throwaway schema, retrying the transient catalog
// races parallel teardowns can hit: PostgreSQL raises XX000 "tuple
// concurrently updated" (and occasionally a 40P01 deadlock) when concurrent
// DROP SCHEMA ... CASCADE statements touch shared catalog rows.
func dropTestSchema(ctx context.Context, admin *pgxpool.Pool, schema string) error {
	const attempts = 5
	var err error
	for attempt := 1; ; attempt++ {
		_, err = admin.Exec(ctx, `DROP SCHEMA `+quoteIdentifier(schema)+` CASCADE`)
		if err == nil || !isTransientCatalogRace(err) || attempt == attempts {
			return err
		}
		time.Sleep(time.Duration(attempt) * 100 * time.Millisecond)
	}
}

func isTransientCatalogRace(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	if pgErr.Code == "40P01" { // deadlock_detected
		return true
	}
	return pgErr.Code == "XX000" && strings.Contains(pgErr.Message, "tuple concurrently updated")
}

package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite" // Pure Go SQLite driver
)

// sqliteTuningPragmas size the connection's caches and bound its files. The
// modernc driver runs each _pragma on every new connection.
//   - cache_size: 64 MiB page cache instead of the 2 MiB default.
//   - mmap_size: reads up to 256 MiB of the file through the OS page cache.
//   - journal_size_limit: truncates the WAL to 64 MiB when it resets, so it
//     shrinks again after large retention deletes. A long-lived reader can
//     delay the reset.
//   - analysis_limit: ANALYZE reads at most 100k entries per index, so it
//     stays well under a second however large a table grows. A lower limit
//     caps the estimated rows per key too far: at 1000, a day-range usage
//     query still chose the model index over the timestamp index.
const sqliteTuningPragmas = "&_pragma=cache_size(-65536)" +
	"&" + sqliteMmapPragma +
	"&_pragma=journal_size_limit(67108864)" +
	"&_pragma=analysis_limit(100000)"

// sqliteMmapPragma is shared with the read pool, see sqliteReaderPragmas.
const sqliteMmapPragma = "_pragma=mmap_size(268435456)"

const (
	// sqliteOptimizeInterval is how often the planner statistics are checked.
	sqliteOptimizeInterval = time.Hour
	// sqliteOptimizeTimeout bounds one check, which holds the writer
	// connection. It stays well under the 30-second usage and audit flush
	// deadlines and bounds how long the startup check delays serving.
	sqliteOptimizeTimeout = 10 * time.Second
	// sqliteOptimize runs ANALYZE (0x02) on every table (0x10000) whose
	// statistics are missing or whose row count changed tenfold since it was
	// last analyzed, so it is cheap when nothing changed. It leaves out 0x10,
	// which would lower analysis_limit to 2000. Without statistics the planner
	// picks poor indexes for the usage and audit log queries.
	sqliteOptimize = "PRAGMA optimize=0x10002"
)

// sqliteStorage implements Storage for SQLite
type sqliteStorage struct {
	db *sql.DB
	// readDB is the read-only pool behind ReadDB; it is db itself for an
	// in-memory database.
	readDB *sql.DB

	maintenanceCtx  context.Context
	stopMaintenance context.CancelFunc
	maintenanceOnce sync.Once
	maintenanceMu   sync.Mutex // orders worker registration against Close
	maintenance     sync.WaitGroup
}

// NewSQLite creates a new SQLite storage connection.
// It enables WAL mode for better concurrent read/write performance.
func NewSQLite(cfg SQLiteConfig) (SQLiteStorage, error) {
	if cfg.Path == "" {
		cfg.Path = DefaultSQLitePath()
	}

	// Ensure directory exists
	dir := filepath.Dir(cfg.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	// Open database with WAL mode and busy timeout
	dsn := fmt.Sprintf("%s?_journal=WAL&_busy_timeout=5000&_synchronous=NORMAL%s", cfg.Path, sqliteTuningPragmas)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open SQLite database: %w", err)
	}

	// Serialize all SQLite writes through a single connection.
	// SQLite only permits one writer at a time. With multiple open connections,
	// concurrent flush loops (audit log + usage tracking) cause SQLITE_BUSY errors.
	// A single connection lets database/sql queue callers in Go, eliminating contention.
	// Read-only stores use the separate pool behind ReadDB instead.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	// Verify connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping SQLite database: %w", err)
	}

	// Set busy_timeout explicitly as defense-in-depth. The DSN parameter may not be
	// honored reliably by the pure-Go driver across pooled connections.
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to set busy_timeout pragma: %w", err)
	}

	readDB, err := openSQLiteReader(db)
	if err != nil {
		db.Close()
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &sqliteStorage{db: db, readDB: readDB, maintenanceCtx: ctx, stopMaintenance: cancel}, nil
}

func (s *sqliteStorage) DB() *sql.DB {
	return s.db
}

// Ping verifies connectivity to the SQLite database.
func (s *sqliteStorage) Ping(ctx context.Context) error {
	if s.db == nil {
		return fmt.Errorf("sqlite database is not initialized")
	}
	return s.db.PingContext(ctx)
}

func (s *sqliteStorage) Close() error {
	if s.stopMaintenance != nil {
		s.maintenanceMu.Lock()
		s.stopMaintenance()
		s.maintenanceMu.Unlock()
		s.maintenance.Wait()
	}
	var errs []error
	if s.readDB != nil && s.readDB != s.db {
		errs = append(errs, s.readDB.Close())
	}
	if s.db != nil {
		errs = append(errs, s.db.Close())
	}
	return errors.Join(errs...)
}

// startMaintenance checks the planner statistics before returning, so the
// first requests do not queue behind it on the single connection, and then
// hourly until Close. Only the first call does anything.
func (s *sqliteStorage) startMaintenance() {
	s.maintenanceOnce.Do(func() {
		if s.maintenanceCtx == nil || s.maintenanceCtx.Err() != nil {
			return
		}
		warned := false
		s.optimize(&warned)

		s.maintenanceMu.Lock()
		defer s.maintenanceMu.Unlock()
		if s.maintenanceCtx.Err() != nil {
			return
		}
		s.maintenance.Go(func() {
			ticker := time.NewTicker(sqliteOptimizeInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					s.optimize(&warned)
				case <-s.maintenanceCtx.Done():
					return
				}
			}
		})
	})
}

// optimize runs one PRAGMA optimize. A failure only costs query plans, so it
// is logged, at warn level the first time and at debug level after that.
func (s *sqliteStorage) optimize(warned *bool) {
	ctx, cancel := context.WithTimeout(s.maintenanceCtx, sqliteOptimizeTimeout)
	defer cancel()
	started := time.Now()
	if _, err := s.db.ExecContext(ctx, sqliteOptimize); err != nil {
		if s.maintenanceCtx.Err() != nil {
			return
		}
		if !*warned {
			*warned = true
			slog.Warn("failed to refresh SQLite query planner statistics", "error", err)
			return
		}
		slog.Debug("failed to refresh SQLite query planner statistics", "error", err)
		return
	}
	slog.Debug("refreshed SQLite query planner statistics", "duration", time.Since(started).Round(time.Millisecond))
	s.reopenIdleReaders()
}

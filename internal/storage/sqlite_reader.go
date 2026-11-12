package storage

import (
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

// sqliteReaderPragmas configure every read connection. The writer's
// journal_size_limit and analysis_limit are left out: they only take effect
// on a connection that writes.
//   - query_only: rejects every statement that would change the database.
//   - cache_size: 8 MiB a connection, so a full pool caches no more than the
//     writer's 64 MiB.
//   - mmap_size: the writer's, see sqliteTuningPragmas.
const sqliteReaderPragmas = "_busy_timeout=5000" +
	"&_pragma=query_only(1)" +
	"&_pragma=cache_size(-8192)" +
	"&" + sqliteMmapPragma

// sqliteReaderConns sizes the read-only pool: enough for a few concurrent
// dashboard scans and budget checks, capped so idle connections stay cheap.
func sqliteReaderConns() int {
	return min(max(4, runtime.GOMAXPROCS(0)), 8)
}

// openSQLiteReader opens a pool of read-only connections to the database the
// writer has open. In WAL mode readers block neither the writer nor each
// other, so dashboard scans, budget checks and live-view polls stop queueing
// the usage and audit flushes behind them on the single writer connection.
//
// A reader sees the database as of the start of its query: a commit still in
// flight on the writer shows up on its next query. Only stores whose reads
// tolerate that use ReadDB; spend and dashboards already trail requests by the
// usage flush interval.
//
// An in-memory database exists only on the connection that created it, so a
// second pool would open a different, empty database. It reads through the
// writer instead.
func openSQLiteReader(writer *sql.DB) (*sql.DB, error) {
	var file string
	if err := writer.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&file); err != nil {
		return nil, fmt.Errorf("failed to inspect SQLite database: %w", err)
	}
	if file == "" {
		return writer, nil
	}

	// The writer already switched the file to WAL, which persists.
	db, err := sql.Open("sqlite", sqliteReaderDSN(file))
	if err != nil {
		return nil, fmt.Errorf("failed to open SQLite read pool: %w", err)
	}
	conns := sqliteReaderConns()
	db.SetMaxOpenConns(conns)
	db.SetMaxIdleConns(conns)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping SQLite read pool: %w", err)
	}
	return db, nil
}

// sqliteReaderDSN builds a file: URI for the resolved database path. The path
// is percent-encoded so a '?', '#' or '%' in it stays part of the file name
// instead of starting the query or fragment, or being decoded by SQLite.
func sqliteReaderDSN(file string) string {
	path := filepath.ToSlash(file)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path // a Windows drive path: file:///C:/...
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: sqliteReaderPragmas}
	return u.String()
}

// ReadDB returns the read-only connection pool. Writes and reads that must
// run in the same transaction as a write use DB.
func (s *sqliteStorage) ReadDB() *sql.DB {
	return s.readDB
}

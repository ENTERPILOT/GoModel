package responsecache

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const sqliteVecCleanupInterval = time.Hour

// sqliteVecStore is a VecStore backed by sqlite-vec.
// With CGO_ENABLED=0 the driver is ncruces/go-sqlite3 (WASM). With CGO_ENABLED=1
// and a C toolchain, the build uses mattn/go-sqlite3 and the CGO sqlite-vec extension.
//
// Schema:
//
//	vec_items(key TEXT, embedding BLOB, params_hash TEXT, response BLOB, expires_at INTEGER)
//
// Uniqueness on (key, params_hash) matches semantic-cache lookup (filter by params_hash, row by key).
// Indexes: unique (key, params_hash); composite (params_hash, expires_at) for Search; expires_at for cleanup.
//
// expires_at is stored as Unix seconds. Search excludes expired rows in SQL.
// A background goroutine calls DeleteExpired every hour.
type sqliteVecStore struct {
	db     *sql.DB
	stopCh chan struct{}
	wg     sync.WaitGroup
}

func openSQLiteVecDB(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("vecstore sqlite: create dir %s: %w", filepath.Dir(path), err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("vecstore sqlite: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if err := configureSQLiteVecJournalMode(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS vec_items (
			key         TEXT    NOT NULL,
			embedding   BLOB    NOT NULL,
			params_hash TEXT    NOT NULL,
			response    BLOB    NOT NULL,
			expires_at  INTEGER NOT NULL
		)
	`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("vecstore sqlite: create table: %w", err)
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_vec_items_key_params ON vec_items (key, params_hash)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("vecstore sqlite: create unique index key_params: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_vec_items_params_expires ON vec_items (params_hash, expires_at)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("vecstore sqlite: create index params_expires: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_vec_items_expires ON vec_items (expires_at)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("vecstore sqlite: create index expires: %w", err)
	}
	return db, nil
}

func configureSQLiteVecJournalMode(db *sql.DB) error {
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		slog.Warn("vecstore sqlite: WAL unavailable; using DELETE journal mode", "err", err)
		if _, err := db.Exec(`PRAGMA journal_mode=DELETE`); err != nil {
			return fmt.Errorf("vecstore sqlite: journal mode DELETE: %w", err)
		}
	}
	return nil
}

func (s *sqliteVecStore) Search(ctx context.Context, vec []float32, paramsHash string, limit int) ([]VecResult, error) {
	now := time.Now().Unix()

	rows, err := s.db.QueryContext(ctx, `
		SELECT key, embedding, response,
		       vec_distance_cosine(embedding, ?) AS distance
		FROM vec_items
		WHERE params_hash = ?
		  AND (expires_at = 0 OR expires_at >= ?)
		ORDER BY distance ASC
		LIMIT ?
	`, serializeFloat32(vec), paramsHash, now, limit)
	if err != nil {
		return nil, fmt.Errorf("vecstore sqlite: search: %w", err)
	}
	defer rows.Close()

	var results []VecResult
	for rows.Next() {
		var (
			key      string
			embBlob  []byte
			response []byte
			distance float64
		)
		if err := rows.Scan(&key, &embBlob, &response, &distance); err != nil {
			return nil, fmt.Errorf("vecstore sqlite: scan row: %w", err)
		}
		_ = embBlob
		results = append(results, VecResult{
			Key:      key,
			Score:    float32(1.0 - distance),
			Response: response,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("vecstore sqlite: rows: %w", err)
	}

	return results, nil
}

func (s *sqliteVecStore) Insert(ctx context.Context, key string, vec []float32, response []byte, paramsHash string, ttl time.Duration) error {
	var expiresAt int64
	if ttl > 0 {
		expiresAt = time.Now().Add(ttl).Unix()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO vec_items (key, embedding, params_hash, response, expires_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (key, params_hash) DO UPDATE SET
			embedding = excluded.embedding,
			response = excluded.response,
			expires_at = excluded.expires_at
	`, key, serializeFloat32(vec), paramsHash, response, expiresAt)
	if err != nil {
		return fmt.Errorf("vecstore sqlite: insert: %w", err)
	}
	return nil
}

func (s *sqliteVecStore) DeleteExpired(ctx context.Context) error {
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx, `DELETE FROM vec_items WHERE expires_at > 0 AND expires_at < ?`, now)
	if err != nil {
		return fmt.Errorf("vecstore sqlite: delete expired: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		slog.Debug("vecstore sqlite: deleted expired entries", "count", n)
	}
	return nil
}

func (s *sqliteVecStore) Close() error {
	close(s.stopCh)
	s.wg.Wait()
	return s.db.Close()
}

func (s *sqliteVecStore) cleanupLoop() {
	defer s.wg.Done()
	t := time.NewTicker(sqliteVecCleanupInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if err := s.DeleteExpired(context.Background()); err != nil {
				slog.Warn("vecstore sqlite: cleanup error", "err", err)
			}
		case <-s.stopCh:
			return
		}
	}
}

func serializeFloat32(vec []float32) []byte {
	buf := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

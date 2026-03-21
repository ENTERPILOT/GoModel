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

	_ "github.com/asg017/sqlite-vec-go-bindings/ncruces"
	_ "github.com/ncruces/go-sqlite3/driver"
)

const sqliteVecCleanupInterval = time.Hour

// sqliteVecStore is a VecStore backed by sqlite-vec (WASM, CGO-free).
//
// Schema:
//
//	vec_items(key TEXT, embedding FLOAT[N], params_hash TEXT, response BLOB, expires_at INTEGER)
//
// expires_at is stored as Unix seconds. Search excludes expired rows in SQL.
// A background goroutine calls DeleteExpired every hour.
type sqliteVecStore struct {
	db     *sql.DB
	stopCh chan struct{}
	wg     sync.WaitGroup
}

func newSQLiteVecStore(path string) (*sqliteVecStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("vecstore sqlite: create dir %s: %w", filepath.Dir(path), err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("vecstore sqlite: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("vecstore sqlite: enable WAL: %w", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS vec_items (
			key        TEXT    NOT NULL,
			embedding  BLOB    NOT NULL,
			params_hash TEXT   NOT NULL,
			response   BLOB    NOT NULL,
			expires_at INTEGER NOT NULL
		)
	`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("vecstore sqlite: create table: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_vec_items_expires ON vec_items(expires_at)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("vecstore sqlite: create index: %w", err)
	}
	s := &sqliteVecStore{db: db, stopCh: make(chan struct{})}
	s.wg.Add(1)
	go s.cleanupLoop()
	return s, nil
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

// serializeFloat32 encodes a float32 slice as little-endian bytes,
// matching the format sqlite-vec expects for KNN queries.
func serializeFloat32(vec []float32) []byte {
	buf := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

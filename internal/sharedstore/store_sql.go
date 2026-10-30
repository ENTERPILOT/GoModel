package sharedstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// sqlBackend keeps entries in SQLite or PostgreSQL.
type sqlBackend struct{ db sqlx.DB }

var sqlSchema = []string{
	`CREATE TABLE IF NOT EXISTS extension_shared_entries (
		namespace TEXT NOT NULL,
		key TEXT NOT NULL,
		value ` + sqlx.TypeBytes + ` NOT NULL,
		expires_at ` + sqlx.TypeInt64 + ` NOT NULL DEFAULT 0,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL,
		PRIMARY KEY (namespace, key)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_extension_shared_entries_expires_at ON extension_shared_entries(expires_at)`,
}

func newSQLBackend(ctx context.Context, db sqlx.DB) (*sqlBackend, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if err := db.Schema(ctx, sqlSchema...); err != nil {
		return nil, fmt.Errorf("create extension_shared_entries table: %w", err)
	}
	return &sqlBackend{db: db}, nil
}

// live matches entries that never expire or expire after the bound now.
const live = `(expires_at = 0 OR expires_at > ?)`

func (b *sqlBackend) put(ctx context.Context, namespace, key string, value []byte, expiresAt, now int64) error {
	_, err := b.db.Exec(ctx, `
		INSERT INTO extension_shared_entries (namespace, key, value, expires_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(namespace, key) DO UPDATE SET
			value = excluded.value,
			expires_at = excluded.expires_at,
			updated_at = excluded.updated_at
	`, namespace, key, value, expiresAt, now)
	if err != nil {
		return fmt.Errorf("put shared entry %s/%q: %w", namespace, key, err)
	}
	return nil
}

func (b *sqlBackend) get(ctx context.Context, namespace, key string, now int64) ([]byte, bool, error) {
	var value []byte
	err := b.db.QueryRow(ctx, `
		SELECT value FROM extension_shared_entries
		WHERE namespace = ? AND key = ? AND `+live,
		namespace, key, now).Scan(&value)
	if errors.Is(err, sqlx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get shared entry %s/%q: %w", namespace, key, err)
	}
	return nonNil(value), true, nil
}

func (b *sqlBackend) remove(ctx context.Context, namespace, key string) error {
	_, err := b.db.Exec(ctx, `DELETE FROM extension_shared_entries WHERE namespace = ? AND key = ?`, namespace, key)
	if err != nil {
		return fmt.Errorf("delete shared entry %s/%q: %w", namespace, key, err)
	}
	return nil
}

func (b *sqlBackend) list(ctx context.Context, namespace string, now int64) ([]ext.SharedEntry, error) {
	rows, err := b.db.Query(ctx, `
		SELECT key, value, expires_at FROM extension_shared_entries
		WHERE namespace = ? AND `+live,
		namespace, now)
	if err != nil {
		return nil, fmt.Errorf("list shared namespace %s: %w", namespace, err)
	}
	defer rows.Close()
	var entries []ext.SharedEntry
	for rows.Next() {
		var entry ext.SharedEntry
		var expiresAt int64
		if err := rows.Scan(&entry.Key, &entry.Value, &expiresAt); err != nil {
			return nil, fmt.Errorf("scan shared entry: %w", err)
		}
		entry.Value = nonNil(entry.Value)
		entry.ExpiresAt = storage.UnixTime(expiresAt)
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list shared namespace %s: %w", namespace, err)
	}
	return entries, nil
}

func (b *sqlBackend) deleteExpired(ctx context.Context, now int64) error {
	_, err := b.db.Exec(ctx, `DELETE FROM extension_shared_entries WHERE expires_at > 0 AND expires_at <= ?`, now)
	if err != nil {
		return fmt.Errorf("delete expired shared entries: %w", err)
	}
	return nil
}

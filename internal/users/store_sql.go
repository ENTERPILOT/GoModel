package users

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// SQLStore stores user policies in a SQL database.
type SQLStore struct {
	db sqlx.DB
}

var sqlTable = `CREATE TABLE IF NOT EXISTS users (
		user_path TEXT PRIMARY KEY,
		allowed_models ` + sqlx.TypeJSONText + ` NOT NULL DEFAULT '[]',
		description TEXT NOT NULL DEFAULT '',
		created_at ` + sqlx.TypeInt64 + ` NOT NULL,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL
	)`

// NewSQLStore creates the users table if needed.
func NewSQLStore(ctx context.Context, db sqlx.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if err := db.Schema(ctx, sqlTable); err != nil {
		return nil, fmt.Errorf("failed to create users table: %w", err)
	}
	return &SQLStore{db: db}, nil
}

func (s *SQLStore) List(ctx context.Context) ([]User, error) {
	rows, err := s.db.Query(ctx, `
		SELECT user_path, allowed_models, description, created_at, updated_at
		FROM users
		ORDER BY user_path ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	result := make([]User, 0)
	for rows.Next() {
		var user User
		var allowedJSON string
		var createdAt, updatedAt int64
		if err := rows.Scan(&user.UserPath, &allowedJSON, &user.Description, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		if user.AllowedModels, err = decodeAllowedModels(allowedJSON); err != nil {
			return nil, err
		}
		user.CreatedAt = sqlutil.TimeFromUnix(createdAt)
		user.UpdatedAt = sqlutil.TimeFromUnix(updatedAt)
		result = append(result, user)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate users: %w", err)
	}
	return result, nil
}

func (s *SQLStore) Upsert(ctx context.Context, user User) error {
	allowedJSON, err := encodeAllowedModels(user.AllowedModels)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO users (user_path, allowed_models, description, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(user_path) DO UPDATE SET
			allowed_models = excluded.allowed_models,
			description = excluded.description,
			updated_at = excluded.updated_at
	`, user.UserPath, allowedJSON, user.Description, user.CreatedAt.Unix(), user.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("upsert user: %w", err)
	}
	return nil
}

func (s *SQLStore) Delete(ctx context.Context, userPath string) error {
	affected, err := s.db.Exec(ctx, `DELETE FROM users WHERE user_path = ?`, userPath)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) Close() error {
	return nil
}

func encodeAllowedModels(values []string) (string, error) {
	if values == nil {
		values = []string{}
	}
	data, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("encode allowed_models: %w", err)
	}
	return string(data), nil
}

func decodeAllowedModels(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("decode allowed_models: %w", err)
	}
	if len(values) == 0 {
		return nil, nil
	}
	return values, nil
}

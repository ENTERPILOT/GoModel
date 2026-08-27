package users

import (
	"context"
	"fmt"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// SQLStore stores users and groups in a SQL database.
type SQLStore struct {
	db sqlx.DB
}

// The group membership column is named user_groups (not "groups"): GROUPS is
// a window-frame keyword in PostgreSQL 11+ and would need quoting everywhere.
var sqlTables = []string{
	`CREATE TABLE IF NOT EXISTS users (
		id TEXT PRIMARY KEY,
		user_path TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL DEFAULT '',
		description TEXT NOT NULL DEFAULT '',
		user_groups ` + sqlx.TypeJSON + `,
		created_at ` + sqlx.TypeInt64 + ` NOT NULL,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS user_groups (
		name TEXT PRIMARY KEY,
		description TEXT NOT NULL DEFAULT '',
		created_at ` + sqlx.TypeInt64 + ` NOT NULL,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL
	)`,
}

var sqlIndexes = []string{
	`CREATE INDEX IF NOT EXISTS idx_users_created_at ON users(created_at DESC)`,
}

// NewSQLStore creates the users and user_groups tables if needed.
func NewSQLStore(ctx context.Context, db sqlx.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if err := db.Schema(ctx, sqlTables...); err != nil {
		return nil, fmt.Errorf("failed to create users tables: %w", err)
	}
	if err := db.Schema(ctx, sqlIndexes...); err != nil {
		return nil, fmt.Errorf("failed to create users index: %w", err)
	}
	return &SQLStore{db: db}, nil
}

func (s *SQLStore) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, user_path, name, description, user_groups, created_at, updated_at
		FROM users
		ORDER BY user_path ASC, id ASC
	`)
	if err != nil {
		return nil, wrapStoreErr("list users", err)
	}
	defer rows.Close()
	result, err := collectRows(rows, scanSQLUser)
	if err != nil {
		return nil, wrapStoreErr("iterate users", err)
	}
	return result, nil
}

func (s *SQLStore) UpsertUser(ctx context.Context, user User) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO users (id, user_path, name, description, user_groups, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			user_path = excluded.user_path,
			name = excluded.name,
			description = excluded.description,
			user_groups = excluded.user_groups,
			updated_at = excluded.updated_at
	`, user.ID, user.UserPath, user.Name, user.Description,
		sqlutil.NullableJSONStrings(user.Groups, user.ID),
		user.CreatedAt.Unix(), user.UpdatedAt.Unix())
	if err != nil {
		return wrapStoreErr("upsert user", err)
	}
	return nil
}

func (s *SQLStore) DeleteUser(ctx context.Context, id string) error {
	affected, err := s.db.Exec(ctx, `DELETE FROM users WHERE id = ?`, normalizeID(id))
	if err != nil {
		return wrapStoreErr("delete user", err)
	}
	if affected == 0 {
		return ErrUserNotFound
	}
	return nil
}

func (s *SQLStore) ListGroups(ctx context.Context) ([]Group, error) {
	rows, err := s.db.Query(ctx, `
		SELECT name, description, created_at, updated_at
		FROM user_groups
		ORDER BY name ASC
	`)
	if err != nil {
		return nil, wrapStoreErr("list groups", err)
	}
	defer rows.Close()
	result, err := collectRows(rows, scanSQLGroup)
	if err != nil {
		return nil, wrapStoreErr("iterate groups", err)
	}
	return result, nil
}

func (s *SQLStore) UpsertGroup(ctx context.Context, group Group) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO user_groups (name, description, created_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			description = excluded.description,
			updated_at = excluded.updated_at
	`, group.Name, group.Description, group.CreatedAt.Unix(), group.UpdatedAt.Unix())
	if err != nil {
		return wrapStoreErr("upsert group", err)
	}
	return nil
}

func (s *SQLStore) DeleteGroup(ctx context.Context, name string) error {
	affected, err := s.db.Exec(ctx, `DELETE FROM user_groups WHERE name = ?`, name)
	if err != nil {
		return wrapStoreErr("delete group", err)
	}
	if affected == 0 {
		return ErrGroupNotFound
	}
	return nil
}

func (s *SQLStore) Close() error {
	return nil
}

func scanSQLUser(scanner userScanner) (User, error) {
	var user User
	var groupsJSON *string
	var createdAt, updatedAt int64
	if err := scanner.Scan(
		&user.ID,
		&user.UserPath,
		&user.Name,
		&user.Description,
		&groupsJSON,
		&createdAt,
		&updatedAt,
	); err != nil {
		return User{}, err
	}
	if groupsJSON != nil {
		user.Groups = sqlutil.StringsFromJSON(*groupsJSON, user.ID)
	}
	user.CreatedAt = time.Unix(createdAt, 0).UTC()
	user.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return user, nil
}

func scanSQLGroup(scanner userScanner) (Group, error) {
	var group Group
	var createdAt, updatedAt int64
	if err := scanner.Scan(&group.Name, &group.Description, &createdAt, &updatedAt); err != nil {
		return Group{}, err
	}
	group.CreatedAt = time.Unix(createdAt, 0).UTC()
	group.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return group, nil
}

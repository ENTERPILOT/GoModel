package users

import (
	"context"
	"fmt"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// SQLStore stores users and groups in a SQL database.
type SQLStore struct {
	db sqlx.DB
}

// The membership column is named user_group (not "group"): GROUP is a SQL
// keyword and would need quoting everywhere; the groups table is user_groups
// for the same reason (GROUPS is a window-frame keyword in PostgreSQL 11+).
var sqlTables = []string{
	`CREATE TABLE IF NOT EXISTS users (
		id TEXT PRIMARY KEY,
		user_path TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL DEFAULT '',
		description TEXT NOT NULL DEFAULT '',
		user_group TEXT NOT NULL DEFAULT '',
		created_at ` + sqlx.TypeInt64 + ` NOT NULL,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS user_groups (
		name TEXT PRIMARY KEY,
		description TEXT NOT NULL DEFAULT '',
		parent TEXT NOT NULL DEFAULT '',
		created_at ` + sqlx.TypeInt64 + ` NOT NULL,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL
	)`,
}

var sqlIndexes = []string{
	`CREATE INDEX IF NOT EXISTS idx_users_created_at ON users(created_at DESC)`,
}

// sqlMigrations backfill columns on tables created before paths became
// derived from the group tree (the earlier schema kept a JSON membership
// column on users and no parent on groups).
var sqlMigrations = []string{
	`ALTER TABLE users ADD COLUMN user_group TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE user_groups ADD COLUMN parent TEXT NOT NULL DEFAULT ''`,
}

// NewSQLStore creates the users and user_groups tables if needed.
func NewSQLStore(ctx context.Context, db sqlx.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if err := db.Schema(ctx, sqlTables...); err != nil {
		return nil, fmt.Errorf("failed to create users tables: %w", err)
	}
	if err := sqlx.AddColumns(ctx, db, sqlMigrations...); err != nil {
		return nil, err
	}
	if err := db.Schema(ctx, sqlIndexes...); err != nil {
		return nil, fmt.Errorf("failed to create users index: %w", err)
	}
	return &SQLStore{db: db}, nil
}

func (s *SQLStore) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, user_path, name, description, user_group, created_at, updated_at
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
	return upsertSQLUser(ctx, s.db, user)
}

func upsertSQLUser(ctx context.Context, q sqlx.Querier, user User) error {
	_, err := q.Exec(ctx, `
		INSERT INTO users (id, user_path, name, description, user_group, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			user_path = excluded.user_path,
			name = excluded.name,
			description = excluded.description,
			user_group = excluded.user_group,
			updated_at = excluded.updated_at
	`, user.ID, user.UserPath, user.Name, user.Description, user.Group,
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
		SELECT name, description, parent, created_at, updated_at
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
	return upsertSQLGroup(ctx, s.db, group)
}

func upsertSQLGroup(ctx context.Context, q sqlx.Querier, group Group) error {
	_, err := q.Exec(ctx, `
		INSERT INTO user_groups (name, description, parent, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			description = excluded.description,
			parent = excluded.parent,
			updated_at = excluded.updated_at
	`, group.Name, group.Description, group.Parent, group.CreatedAt.Unix(), group.UpdatedAt.Unix())
	if err != nil {
		return wrapStoreErr("upsert group", err)
	}
	return nil
}

// ApplyGroupMove writes the group and the member path rewrites in one
// transaction, so an interrupted cascade rolls back instead of persisting a
// moved group alongside stale user paths.
func (s *SQLStore) ApplyGroupMove(ctx context.Context, group Group, rewrites []User) error {
	return s.db.InTx(ctx, func(q sqlx.Querier) error {
		if err := upsertSQLGroup(ctx, q, group); err != nil {
			return err
		}
		for _, user := range rewrites {
			if err := upsertSQLUser(ctx, q, user); err != nil {
				return err
			}
		}
		return nil
	})
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
	var createdAt, updatedAt int64
	if err := scanner.Scan(
		&user.ID,
		&user.UserPath,
		&user.Name,
		&user.Description,
		&user.Group,
		&createdAt,
		&updatedAt,
	); err != nil {
		return User{}, err
	}
	user.CreatedAt = time.Unix(createdAt, 0).UTC()
	user.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return user, nil
}

func scanSQLGroup(scanner userScanner) (Group, error) {
	var group Group
	var createdAt, updatedAt int64
	if err := scanner.Scan(&group.Name, &group.Description, &group.Parent, &createdAt, &updatedAt); err != nil {
		return Group{}, err
	}
	group.CreatedAt = time.Unix(createdAt, 0).UTC()
	group.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return group, nil
}

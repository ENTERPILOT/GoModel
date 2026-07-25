package virtualmodels

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// SQLStore stores virtual models in a SQL database.
type SQLStore struct {
	db sqlx.DB
}

var sqlSchema = []string{
	`CREATE TABLE IF NOT EXISTS virtual_models (
		source TEXT PRIMARY KEY,
		targets TEXT NOT NULL DEFAULT '[]',
		strategy TEXT NOT NULL DEFAULT '',
		provider_name TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		user_paths TEXT NOT NULL DEFAULT '[]',
		description TEXT NOT NULL DEFAULT '',
		enabled ` + sqlx.TypeBool + ` NOT NULL DEFAULT TRUE,
		created_at ` + sqlx.TypeInt64 + ` NOT NULL,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_virtual_models_provider_name ON virtual_models(provider_name)`,
	`CREATE INDEX IF NOT EXISTS idx_virtual_models_model ON virtual_models(model)`,
	`CREATE INDEX IF NOT EXISTS idx_virtual_models_enabled ON virtual_models(enabled)`,
	`CREATE INDEX IF NOT EXISTS idx_virtual_models_updated_at ON virtual_models(updated_at DESC)`,
}

const selectVirtualModelColumns = `
	SELECT source, targets, strategy, provider_name, model, user_paths,
		description, enabled, created_at, updated_at
	FROM virtual_models
`

const upsertVirtualModelSQL = `
	INSERT INTO virtual_models (
		source, targets, strategy, provider_name, model, user_paths, description, enabled, created_at, updated_at
	)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(source) DO UPDATE SET
		targets = excluded.targets,
		strategy = excluded.strategy,
		provider_name = excluded.provider_name,
		model = excluded.model,
		user_paths = excluded.user_paths,
		description = excluded.description,
		enabled = excluded.enabled,
		updated_at = excluded.updated_at
`

// NewSQLStore creates the virtual_models table and indexes if needed.
func NewSQLStore(ctx context.Context, db sqlx.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if err := db.Schema(ctx, sqlSchema...); err != nil {
		return nil, fmt.Errorf("failed to create virtual_models table: %w", err)
	}
	return &SQLStore{db: db}, nil
}

func (s *SQLStore) List(ctx context.Context) ([]VirtualModel, error) {
	rows, err := s.db.Query(ctx, selectVirtualModelColumns+`ORDER BY source ASC`)
	if err != nil {
		return nil, fmt.Errorf("list virtual models: %w", err)
	}
	defer rows.Close()
	return collectVirtualModels(func() (VirtualModel, bool, error) {
		if !rows.Next() {
			return VirtualModel{}, false, nil
		}
		vm, err := scanSQLVirtualModel(rows)
		return vm, true, err
	}, rows.Err)
}

func (s *SQLStore) Get(ctx context.Context, source string) (*VirtualModel, error) {
	row := s.db.QueryRow(ctx, selectVirtualModelColumns+`WHERE source = ?`, strings.TrimSpace(source))
	vm, err := scanSQLVirtualModel(row)
	if err != nil {
		if errors.Is(err, sqlx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &vm, nil
}

func (s *SQLStore) Upsert(ctx context.Context, vm VirtualModel) error {
	args, err := virtualModelUpsertArgs(vm)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, upsertVirtualModelSQL, args...); err != nil {
		return fmt.Errorf("upsert virtual model: %w", err)
	}
	return nil
}

// UpsertAll writes every row in a single transaction, so a failed seed leaves the
// table untouched rather than partially populated (which would otherwise trip the
// "already populated" guard and suppress a re-import on the next start).
func (s *SQLStore) UpsertAll(ctx context.Context, vms []VirtualModel) error {
	if len(vms) == 0 {
		return nil
	}
	return s.db.InTx(ctx, func(q sqlx.Querier) error {
		for _, vm := range vms {
			args, err := virtualModelUpsertArgs(vm)
			if err != nil {
				return err
			}
			if _, err := q.Exec(ctx, upsertVirtualModelSQL, args...); err != nil {
				return fmt.Errorf("upsert virtual model: %w", err)
			}
		}
		return nil
	})
}

func (s *SQLStore) Delete(ctx context.Context, source string) error {
	affected, err := s.db.Exec(ctx,
		`DELETE FROM virtual_models WHERE source = ?`, strings.TrimSpace(source))
	if err != nil {
		return fmt.Errorf("delete virtual model: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) Close() error {
	return nil
}

func virtualModelUpsertArgs(vm VirtualModel) ([]any, error) {
	stampUpsert(&vm)
	targetsJSON, err := encodeTargets(vm.Targets)
	if err != nil {
		return nil, err
	}
	pathsJSON, err := encodeUserPaths(vm.UserPaths)
	if err != nil {
		return nil, err
	}
	return []any{
		strings.TrimSpace(vm.Source),
		targetsJSON,
		vm.Strategy,
		vm.ProviderName,
		vm.Model,
		pathsJSON,
		vm.Description,
		vm.Enabled,
		vm.CreatedAt.Unix(),
		vm.UpdatedAt.Unix(),
	}, nil
}

func scanSQLVirtualModel(scanner sqlx.Row) (VirtualModel, error) {
	var vm VirtualModel
	var targets, userPaths []byte
	var createdAt, updatedAt int64
	if err := scanner.Scan(
		&vm.Source,
		&targets,
		&vm.Strategy,
		&vm.ProviderName,
		&vm.Model,
		&userPaths,
		&vm.Description,
		&vm.Enabled,
		&createdAt,
		&updatedAt,
	); err != nil {
		return VirtualModel{}, err
	}
	var err error
	if vm.Targets, err = decodeTargets(targets); err != nil {
		return VirtualModel{}, err
	}
	if vm.UserPaths, err = decodeUserPaths(userPaths); err != nil {
		return VirtualModel{}, err
	}
	vm.CreatedAt = time.Unix(createdAt, 0).UTC()
	vm.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return vm, nil
}

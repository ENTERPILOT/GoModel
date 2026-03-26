package executionplans

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"gomodel/config"
	"gomodel/internal/storage"
)

// Result holds the initialized execution-plan service and any owned resources.
type Result struct {
	Service *Service
	Store   Store
	Storage storage.Storage

	stopRefresh func()
	closeOnce   sync.Once
	closeErr    error
}

// Close releases resources held by the execution-plan subsystem.
func (r *Result) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		if r.stopRefresh != nil {
			r.stopRefresh()
			r.stopRefresh = nil
		}

		var errs []error
		if r.Store != nil {
			if err := r.Store.Close(); err != nil {
				errs = append(errs, fmt.Errorf("store close: %w", err))
			}
		}
		if r.Storage != nil {
			if err := r.Storage.Close(); err != nil {
				errs = append(errs, fmt.Errorf("storage close: %w", err))
			}
		}
		if len(errs) > 0 {
			r.closeErr = fmt.Errorf("close errors: %w", errors.Join(errs...))
		}
	})
	return r.closeErr
}

// New creates an execution-plan subsystem with its own storage connection.
func New(ctx context.Context, cfg *config.Config, compiler Compiler, refreshInterval time.Duration) (*Result, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	storeConn, err := storage.New(ctx, buildStorageConfig(cfg))
	if err != nil {
		return nil, fmt.Errorf("failed to create storage: %w", err)
	}
	result, err := newResult(ctx, storeConn, compiler, refreshInterval)
	if err != nil {
		_ = storeConn.Close()
		return nil, err
	}
	result.Storage = storeConn
	return result, nil
}

// NewWithSharedStorage creates an execution-plan subsystem using an existing storage connection.
func NewWithSharedStorage(ctx context.Context, shared storage.Storage, compiler Compiler, refreshInterval time.Duration) (*Result, error) {
	if shared == nil {
		return nil, fmt.Errorf("shared storage is required")
	}
	return newResult(ctx, shared, compiler, refreshInterval)
}

func newResult(ctx context.Context, storeConn storage.Storage, compiler Compiler, refreshInterval time.Duration) (*Result, error) {
	store, err := createStore(ctx, storeConn)
	if err != nil {
		return nil, err
	}
	service, err := NewService(store, compiler)
	if err != nil {
		return nil, err
	}

	return &Result{
		Service:     service,
		Store:       store,
		stopRefresh: service.StartBackgroundRefresh(refreshInterval),
	}, nil
}

func buildStorageConfig(cfg *config.Config) storage.Config {
	storageCfg := storage.Config{
		Type: cfg.Storage.Type,
		SQLite: storage.SQLiteConfig{
			Path: cfg.Storage.SQLite.Path,
		},
		PostgreSQL: storage.PostgreSQLConfig{
			URL:      cfg.Storage.PostgreSQL.URL,
			MaxConns: cfg.Storage.PostgreSQL.MaxConns,
		},
		MongoDB: storage.MongoDBConfig{
			URL:      cfg.Storage.MongoDB.URL,
			Database: cfg.Storage.MongoDB.Database,
		},
	}

	if storageCfg.Type == "" {
		storageCfg.Type = storage.TypeSQLite
	}
	if storageCfg.SQLite.Path == "" {
		storageCfg.SQLite.Path = storage.DefaultSQLitePath
	}
	if storageCfg.MongoDB.Database == "" {
		storageCfg.MongoDB.Database = "gomodel"
	}
	return storageCfg
}

func createStore(ctx context.Context, store storage.Storage) (Store, error) {
	switch store.Type() {
	case storage.TypeSQLite:
		return NewSQLiteStore(store.SQLiteDB())
	case storage.TypePostgreSQL:
		pool := store.PostgreSQLPool()
		if pool == nil {
			return nil, fmt.Errorf("PostgreSQL pool is nil")
		}
		pgxPool, ok := pool.(*pgxpool.Pool)
		if !ok {
			return nil, fmt.Errorf("invalid PostgreSQL pool type: %T", pool)
		}
		return NewPostgreSQLStore(ctx, pgxPool)
	case storage.TypeMongoDB:
		db := store.MongoDatabase()
		if db == nil {
			return nil, fmt.Errorf("MongoDB database is nil")
		}
		mongoDB, ok := db.(*mongo.Database)
		if !ok {
			return nil, fmt.Errorf("invalid MongoDB database type: %T", db)
		}
		return NewMongoDBStore(mongoDB)
	default:
		return nil, fmt.Errorf("unknown storage type: %s", store.Type())
	}
}

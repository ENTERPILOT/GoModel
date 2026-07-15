package mcpgateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/httpclient"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/usage"
)

// Result holds the initialized MCP gateway and any owned resources.
type Result struct {
	Service *Service
	Store   Store
	Storage storage.Storage

	closeOnce sync.Once
	closeErr  error
}

// Close releases resources held by the MCP gateway subsystem.
func (r *Result) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		if r.Service != nil {
			r.Service.Close()
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

// New creates the MCP gateway subsystem with its own storage connection.
func New(ctx context.Context, cfg *config.Config, httpClient *http.Client, usageLogger usage.LoggerInterface) (*Result, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	storeConn, err := storage.New(ctx, cfg.Storage.BackendConfig())
	if err != nil {
		return nil, fmt.Errorf("failed to create storage: %w", err)
	}
	result, err := newResult(ctx, cfg, storeConn, httpClient, usageLogger)
	if err != nil {
		_ = storeConn.Close()
		return nil, err
	}
	result.Storage = storeConn
	return result, nil
}

// NewWithSharedStorage creates the MCP gateway subsystem using an existing
// storage connection.
func NewWithSharedStorage(ctx context.Context, cfg *config.Config, shared storage.Storage, httpClient *http.Client, usageLogger usage.LoggerInterface) (*Result, error) {
	if shared == nil {
		return nil, fmt.Errorf("shared storage is required")
	}
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	return newResult(ctx, cfg, shared, httpClient, usageLogger)
}

func newResult(ctx context.Context, cfg *config.Config, storeConn storage.Storage, httpClient *http.Client, usageLogger usage.LoggerInterface) (*Result, error) {
	store, err := createStore(ctx, storeConn)
	if err != nil {
		return nil, err
	}
	if httpClient == nil {
		httpClient = defaultUpstreamHTTPClient()
	}

	configSpecs := make(map[string]ServerSpec, len(cfg.MCP.Servers))
	for name, serverCfg := range cfg.MCP.Servers {
		configSpecs[name] = SpecFromConfig(name, serverCfg)
	}

	service, err := NewService(ctx, Options{
		ConfigServers:  configSpecs,
		Store:          store,
		HTTPClient:     httpClient,
		UsageLogger:    usageLogger,
		UserPathHeader: cfg.Server.UserPathHeader,
	})
	if err != nil {
		return nil, err
	}

	return &Result{
		Service: service,
		Store:   store,
	}, nil
}

// defaultUpstreamHTTPClient is the shared pooled client for http/sse
// upstreams. It carries no whole-request timeout: the standalone SSE
// notification stream is expected to outlive HTTP_TIMEOUT, and every
// individual operation is already bounded by a context deadline.
func defaultUpstreamHTTPClient() *http.Client {
	cfg := httpclient.DefaultConfig()
	cfg.Timeout = 0
	return httpclient.NewHTTPClient(&cfg)
}

func createStore(ctx context.Context, store storage.Storage) (Store, error) {
	return storage.ResolveBackend[Store](
		store,
		func(db *sql.DB) (Store, error) { return NewSQLiteStore(db) },
		func(pool *pgxpool.Pool) (Store, error) { return NewPostgreSQLStore(ctx, pool) },
		func(db *mongo.Database) (Store, error) { return NewMongoDBStore(db) },
	)
}

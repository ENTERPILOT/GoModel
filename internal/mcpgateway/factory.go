package mcpgateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/encryption"
	"github.com/enterpilot/gomodel/internal/httpclient"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/usage"
)

// Result holds the initialized MCP gateway and any owned resources.
type Result struct {
	Service *Service
	Store   Store

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
		if len(errs) > 0 {
			r.closeErr = fmt.Errorf("close errors: %w", errors.Join(errs...))
		}
	})
	return r.closeErr
}

// New creates the MCP gateway subsystem using an existing
// storage connection. Header values of managed servers are sealed with box;
// a nil box stores them in plaintext. secrets resolves the secret references
// admin-managed servers hold.
func New(ctx context.Context, cfg *config.Config, shared storage.Storage, box *encryption.Box, httpClient *http.Client, usageLogger usage.LoggerInterface, secrets *config.Secrets) (*Result, error) {
	if shared == nil {
		return nil, fmt.Errorf("shared storage is required")
	}
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	return newResult(ctx, cfg, shared, box, httpClient, usageLogger, secrets)
}

// Reencrypt seals every managed server header that is in plaintext or sealed
// with a data key other than box's active one.
func Reencrypt(ctx context.Context, shared storage.Storage, box *encryption.Box) (encryption.Report, error) {
	store, err := createStore(ctx, shared)
	if err != nil {
		return encryption.Report{Entity: "mcp_servers"}, err
	}
	defer func() { _ = store.Close() }()
	swap, ok := store.(headerSwapper)
	if !ok {
		return encryption.Report{Entity: "mcp_servers"}, fmt.Errorf("mcp server store %T cannot re-encrypt", store)
	}
	return (&sealedStore{Store: store, box: box, swap: swap}).reencrypt(ctx, swap)
}

func newResult(ctx context.Context, cfg *config.Config, storeConn storage.Storage, box *encryption.Box, httpClient *http.Client, usageLogger usage.LoggerInterface, secrets *config.Secrets) (*Result, error) {
	rawStore, err := createStore(ctx, storeConn)
	if err != nil {
		return nil, err
	}
	store := sealStore(rawStore, box)
	if httpClient == nil {
		httpClient = defaultUpstreamHTTPClient()
	}

	configSpecs := make(map[string]ServerSpec, len(cfg.MCP.Servers))
	for name, serverCfg := range cfg.MCP.Servers {
		configSpecs[name] = SpecFromConfig(name, serverCfg)
	}

	virtualSpecs := make(map[string]VirtualServerSpec, len(cfg.MCP.VirtualServers))
	for name, virtualCfg := range cfg.MCP.VirtualServers {
		virtualSpecs[name] = VirtualFromConfig(name, virtualCfg)
	}

	service, err := NewService(ctx, Options{
		ConfigServers:  configSpecs,
		VirtualServers: virtualSpecs,
		Store:          store,
		HTTPClient:     httpClient,
		UsageLogger:    usageLogger,
		UserPathHeader: cfg.Server.UserPathHeader,
		AllowedOrigins: cfg.MCP.AllowedOrigins,
		ToolDiscovery:  cfg.MCP.ToolDiscovery,
		Secrets:        secrets,
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
	return storage.ResolveSQLBackend[Store](
		ctx,
		store,
		func(db sqlx.DB) (Store, error) { return NewSQLStore(ctx, db) },
		func(db *mongo.Database) (Store, error) { return NewMongoDBStore(db) },
	)
}

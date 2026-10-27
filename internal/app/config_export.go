package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/budget"
	"github.com/enterpilot/gomodel/internal/mcpgateway"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/ratelimit"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/tagging"
	"github.com/enterpilot/gomodel/internal/users"
	"github.com/enterpilot/gomodel/internal/version"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

// ConfigExportOptions configures ExportConfig.
type ConfigExportOptions struct {
	// Config is the loaded configuration; its storage section and key
	// wrapper select the database and the key that unseals stored secrets.
	Config *config.LoadResult
	// Factory registers the provider types, whose environment variables
	// declare providers too.
	Factory *providers.ProviderFactory
	// Extensions supplies compiled-in plugins, whose schemas say which
	// guardrail settings are secret.
	Extensions *ext.Registry
	// Environ is the environment variables a secret may be named after.
	// Default: os.Environ().
	Environ []string
	// Now stamps the header. Default: the current time.
	Now time.Time
}

// ExportConfig renders the configuration a gateway started now would run
// with as a config.yaml holding no secret (see config.LoadResult.ExportYAML):
// every setting, from defaults, config.yaml, and environment variables, and
// the objects saved in the dashboard.
func ExportConfig(ctx context.Context, opts ConfigExportOptions) ([]byte, error) {
	loaded := opts.Config
	if loaded == nil || loaded.Config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if opts.Factory == nil {
		return nil, fmt.Errorf("provider factory is required")
	}
	if opts.Environ == nil {
		opts.Environ = os.Environ()
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	// Idempotent: the CLI has already resolved them, an embedder may not.
	if err := loaded.ResolveSecrets(ctx); err != nil {
		return nil, fmt.Errorf("failed to resolve secret references: %w", err)
	}
	declared, err := providers.ExportProviders(ctx, loaded, opts.Factory)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve providers: %w", err)
	}
	stored, err := readStoredObjects(ctx, loaded)
	if err != nil {
		return nil, err
	}
	catalog, err := commandPluginCatalog(loaded.Config, opts.Extensions)
	if err != nil {
		return nil, err
	}

	cfg := *loaded.Config
	var doc config.ExportDocument
	var references []string
	doc.Providers, references = exportProviders(declared, stored.credentials)
	doc.References = append(doc.References, references...)
	cfg.VirtualModels = exportVirtualModels(cfg.VirtualModels, stored.virtualModels)
	cfg.Users = exportUsers(cfg.Users, stored.users)
	cfg.MCP, references = exportMCP(cfg.MCP, stored.mcpServers, stored.mcpVirtual)
	doc.References = append(doc.References, references...)
	cfg.Tagging.Headers = exportTaggingHeaders(cfg.Tagging.Headers, stored.tagging)
	if cfg.Budgets, err = exportBudgets(cfg.Budgets, stored.budgets); err != nil {
		return nil, err
	}
	if cfg.RateLimits, err = exportRateLimits(cfg.RateLimits, stored.rateLimits); err != nil {
		return nil, err
	}
	doc.Config = &cfg
	doc.SecretFields = guardrailSecretFields(cfg.Guardrails.Rules, catalog)
	doc.Header = configExportHeader(opts.Now)
	return loaded.ExportYAML(doc, opts.Environ)
}

// readStoredObjects reads the objects saved in the dashboard from the
// configured database, unsealing their secrets as the gateway does.
func readStoredObjects(ctx context.Context, loaded *config.LoadResult) (storedObjects, error) {
	var stored storedObjects
	appCfg := loaded.Config
	shared, err := storage.New(ctx, appCfg.Storage.BackendConfig())
	if err != nil {
		return stored, fmt.Errorf("failed to open storage: %w", err)
	}
	defer func() { _ = shared.Close() }()
	box, err := openSecretBox(ctx, shared, loaded)
	if err != nil {
		return stored, err
	}

	if stored.credentials, err = providers.ListStoredCredentials(ctx, shared, box); err != nil {
		return stored, fmt.Errorf("read provider credentials: %w", err)
	}
	if stored.virtualModels, err = virtualmodels.ListStored(ctx, shared); err != nil {
		return stored, fmt.Errorf("read virtual models: %w", err)
	}
	if stored.users, err = users.ListStored(ctx, shared); err != nil {
		return stored, fmt.Errorf("read users: %w", err)
	}
	if stored.mcpServers, stored.mcpVirtual, err = mcpgateway.ListStored(ctx, shared, box); err != nil {
		return stored, fmt.Errorf("read MCP servers: %w", err)
	}
	if stored.tagging, err = tagging.ListStored(ctx, shared); err != nil {
		return stored, fmt.Errorf("read tagging rules: %w", err)
	}
	// Budgets and rate limits are stored only while enabled.
	if appCfg.Budgets.Enabled {
		if stored.budgets, err = budget.ListStored(ctx, shared); err != nil {
			return stored, fmt.Errorf("read budgets: %w", err)
		}
	}
	if appCfg.RateLimits.Enabled {
		if stored.rateLimits, err = ratelimit.ListStored(ctx, shared); err != nil {
			return stored, fmt.Errorf("read rate limits: %w", err)
		}
	}
	return stored, nil
}

func configExportHeader(now time.Time) string {
	return strings.Join([]string{
		fmt.Sprintf("GoModel configuration exported %s by %s %s.", now.UTC().Format(time.RFC3339), version.App, version.Version),
		"Every setting in effect: defaults, config.yaml, environment variables,",
		"and objects saved in the dashboard. Load it as config/config.yaml;",
		"objects saved in the dashboard are then read-only there.",
		"",
		"No secret is included. A secret is written as the reference it was",
		"configured with (env, file, or vault), or as an env reference with a",
		"comment naming the variable to set before loading this file.",
		"",
		"Not exported: managed API keys, workflows, guardrails created in the",
		"dashboard, model pricing overrides, runtime settings, and disabled",
		"provider credentials.",
	}, "\n")
}

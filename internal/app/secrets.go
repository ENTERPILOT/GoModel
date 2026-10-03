package app

import (
	"context"
	"fmt"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/encryption"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/mcpgateway"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/storage"
)

// encryptionOptions selects the key-encryption key for one generation: an
// extension's wrapper when set, otherwise GOMODEL_ENCRYPTION_KEY.
func encryptionOptions(loaded *config.LoadResult) encryption.Options {
	return encryption.Options{
		Key:         loaded.Config.Storage.EncryptionKey,
		PreviousKey: loaded.Config.Storage.EncryptionKeyPrevious,
		Wrapper:     loaded.KeyWrapper(),
	}
}

// openSecretBox loads the data keys that seal dashboard-managed secrets.
func openSecretBox(ctx context.Context, shared storage.Storage, loaded *config.LoadResult) (*encryption.Box, error) {
	keys, err := encryption.NewKeyStore(ctx, shared)
	if err != nil {
		return nil, err
	}
	return encryption.Open(ctx, keys, encryptionOptions(loaded))
}

// ReencryptOptions configures ReencryptSecrets.
type ReencryptOptions struct {
	// Config is the loaded configuration; its storage section and key
	// wrapper select the database and the key-encryption key.
	Config *config.LoadResult
	// Extensions supplies compiled-in plugins, whose schemas say which
	// guardrail config values are secret.
	Extensions *ext.Registry
	// RotateDataKey creates and activates a new data key first, so the pass
	// moves every value to it.
	RotateDataKey bool
}

// ReencryptResult is what ReencryptSecrets did. It never carries values.
type ReencryptResult struct {
	// ActiveKeyID is the data key every secret is now sealed with.
	ActiveKeyID string
	Reports     []encryption.Report
}

// ReencryptSecrets seals every dashboard-managed secret stored in plaintext
// and re-seals values under a data key other than the active one. It is
// idempotent: a second run rewrites nothing.
func ReencryptSecrets(ctx context.Context, opts ReencryptOptions) (ReencryptResult, error) {
	var result ReencryptResult
	if opts.Config == nil || opts.Config.Config == nil {
		return result, fmt.Errorf("config is required")
	}
	encOpts := encryptionOptions(opts.Config)
	if encOpts.Key == "" && encOpts.Wrapper == nil {
		return result, fmt.Errorf("GOMODEL_ENCRYPTION_KEY is not set; there is nothing to encrypt with")
	}
	appCfg := opts.Config.Config
	shared, err := storage.New(ctx, appCfg.Storage.BackendConfig())
	if err != nil {
		return result, fmt.Errorf("failed to open storage: %w", err)
	}
	defer func() { _ = shared.Close() }()

	keys, err := encryption.NewKeyStore(ctx, shared)
	if err != nil {
		return result, err
	}
	open := encryption.Open
	if opts.RotateDataKey {
		open = encryption.RotateDataKey
	}
	box, err := open(ctx, keys, encOpts)
	if err != nil {
		return result, err
	}
	result.ActiveKeyID = box.ActiveKeyID()

	catalog, err := reencryptCatalog(appCfg, opts.Extensions)
	if err != nil {
		return result, err
	}
	passes := []func() (encryption.Report, error){
		func() (encryption.Report, error) { return providers.ReencryptCredentials(ctx, shared, box) },
		func() (encryption.Report, error) { return mcpgateway.Reencrypt(ctx, shared, box) },
		func() (encryption.Report, error) { return guardrails.Reencrypt(ctx, shared, box, catalog) },
	}
	for _, pass := range passes {
		report, err := pass()
		result.Reports = append(result.Reports, report)
		if err != nil {
			return result, fmt.Errorf("%s: %w", report.Entity, err)
		}
	}
	return result, nil
}

// reencryptCatalog builds the plugin catalog the gateway would, so plaintext
// guardrail secrets are recognised by their schema. Without the plugin system
// only built-in and compiled-in plugins are known.
func reencryptCatalog(appCfg *config.Config, extensions *ext.Registry) (*plugins.Catalog, error) {
	if pluginsEnabled(appCfg) {
		return buildPluginCatalog(appCfg, extensions)
	}
	return buildPluginCatalog(nil, extensions)
}

package app

import (
	"context"
	"errors"
)

// Entity names in the shared config_revisions table. Each names one
// admin-managed snapshot that every gateway instance reloads when another
// instance changes it. They are stored, so renaming one is a migration.
const (
	syncEntityProviderCredentials = "provider_credentials"
	syncEntityBudgets             = "budgets"
	syncEntityRateLimits          = "rate_limits"
	syncEntityAuthKeys            = "auth_keys"
	syncEntityUsers               = "users"
	syncEntityVirtualModels       = "virtual_models"
	syncEntityPricingOverrides    = "pricing_overrides"
	syncEntityTagging             = "tagging"
	syncEntityGuardrails          = "guardrails"
	syncEntityWorkflows           = "workflows"
	syncEntityMCPServers          = "mcp_servers"
)

// syncedService is an admin-managed snapshot that announces its writes and
// can be reloaded from the database.
type syncedService interface {
	SetChangeNotifier(func(context.Context))
	Refresh(context.Context) error
}

// initClusterSync connects every admin-managed snapshot to the revision
// poller opened in initStorage, so a change saved on any instance reaches
// this one within a few seconds. It runs once every service exists. Polling
// starts when the generation serves (see startServer).
//
// Entities refresh in this order on a tick: provider credentials first,
// because virtual models, users, and pricing overrides validate against the
// provider catalog.
func (b *bootstrap) initClusterSync() error {
	app := b.app
	syncer := app.clusterSync
	if syncer == nil {
		return nil
	}
	wire := func(entity string, service syncedService, refresh func(context.Context) error) {
		if nilInterface(service) {
			return
		}
		if refresh == nil {
			refresh = service.Refresh
		}
		service.SetChangeNotifier(syncer.Notifier(entity))
		syncer.Register(entity, refresh)
	}

	if app.providerCredentials != nil {
		wire(syncEntityProviderCredentials, app.providerCredentials.Service, nil)
	}
	wire(syncEntityBudgets, app.budgets.Service, nil)
	wire(syncEntityRateLimits, app.rateLimits.Service, nil)
	wire(syncEntityAuthKeys, app.authKeys.Service, nil)
	wire(syncEntityUsers, app.users.Service, nil)
	wire(syncEntityVirtualModels, app.virtualModels.Service, nil)
	wire(syncEntityPricingOverrides, app.pricingOverrides.Service, nil)
	wire(syncEntityTagging, app.tagging.Service, nil)
	if app.guardrails != nil && app.guardrails.Service != nil {
		// Workflows compile against the guardrail instances, so they are
		// recompiled onto the reloaded ones, as after a local guardrail save.
		guardrails, workflows := app.guardrails.Service, app.workflows.Service
		wire(syncEntityGuardrails, guardrails, func(ctx context.Context) error {
			return errors.Join(guardrails.Refresh(ctx), workflows.Refresh(ctx))
		})
	}
	wire(syncEntityWorkflows, app.workflows.Service, nil)
	if app.mcpGateway != nil {
		wire(syncEntityMCPServers, app.mcpGateway.Service, nil)
	}
	return nil
}

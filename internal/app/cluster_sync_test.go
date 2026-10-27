package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/budget"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/ratelimit"
)

// newReplica builds one gateway instance on the database at path, as a
// second replica sharing it would, and starts its config sync the way
// startServer does.
func newReplica(t *testing.T, path string) *App {
	t.Helper()
	t.Setenv("STORAGE_TYPE", "sqlite")
	t.Setenv("SQLITE_PATH", path)
	t.Setenv("ADMIN_UI_ENABLED", "false")
	t.Setenv("ADMIN_ENDPOINTS_ENABLED", "false")
	t.Setenv("MCP_ENABLED", "false")
	t.Setenv("USAGE_ENABLED", "true")
	t.Setenv("BUDGETS_ENABLED", "true")
	t.Setenv("RATE_LIMITS_ENABLED", "true")

	loaded, err := config.Load()
	require.NoError(t, err)
	application, err := New(t.Context(), Config{AppConfig: loaded, Factory: providers.NewProviderFactory()})
	require.NoError(t, err)
	t.Cleanup(func() {
		assert.NoError(t, application.Shutdown(context.Background()))
	})
	application.clusterSync.Start(t.Context())
	return application
}

// TestClusterSyncAppliesChangesFromAnotherReplica runs two replicas on one
// SQLite file: budgets and rate limits saved on one are enforced by the other
// after a sync tick, with no restart and no manual refresh.
func TestClusterSyncAppliesChangesFromAnotherReplica(t *testing.T) {
	t.Chdir(t.TempDir())
	path := filepath.Join(t.TempDir(), "shared.db")
	first := newReplica(t, path)
	second := newReplica(t, path)
	ctx := t.Context()
	// One sync interval plus slack for the refresh itself.
	const propagation = 10 * time.Second

	team := budget.Subjects{UserPath: "/team"}
	statuses, err := second.budgets.Service.StatusesFor(ctx, team, time.Now())
	require.NoError(t, err)
	require.Empty(t, statuses)

	err = first.budgets.Service.UpsertBudgets(ctx, []budget.Budget{{
		Scope: budget.ScopeUserPath, Subject: "/team", PeriodSeconds: 86400, Amount: 5, Source: budget.SourceManual,
	}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		statuses, err := second.budgets.Service.StatusesFor(ctx, team, time.Now())
		return err == nil && len(statuses) == 1
	}, propagation, 50*time.Millisecond, "budget created on one replica never reached the other")

	// A reset is a write to the budget row, so it propagates the same way.
	resetAt := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, first.budgets.Service.ResetBudget(ctx, budget.ScopeUserPath, "/team", 86400, resetAt))
	require.Eventually(t, func() bool {
		budgets := second.budgets.Service.Budgets()
		return len(budgets) == 1 && budgets[0].LastResetAt != nil && budgets[0].LastResetAt.Equal(resetAt)
	}, propagation, 50*time.Millisecond, "budget reset on one replica never reached the other")

	one := int64(1)
	err = first.rateLimits.Service.UpsertRules(ctx, []ratelimit.Rule{{
		Scope: ratelimit.ScopeUserPath, Subject: "/team", PeriodSeconds: 60, MaxRequests: &one, Source: "manual",
	}})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(second.rateLimits.Service.Rules()) == 1 },
		propagation, 50*time.Millisecond, "rate limit rule created on one replica never reached the other")
	subjects := ratelimit.Subjects{UserPath: "/team"}
	_, err = second.rateLimits.Service.Acquire(subjects, time.Now())
	require.NoError(t, err)
	_, err = second.rateLimits.Service.Acquire(subjects, time.Now())
	require.Error(t, err, "the replica that did not save the rule must enforce it")
}

// Every admin-managed snapshot must be wired, or a change to it would apply
// on the instance that saved it only.
func TestClusterSyncWiresEveryAdminManagedSnapshot(t *testing.T) {
	application := newFullyWiredApp(t)
	assert.Equal(t, []string{
		syncEntityProviderCredentials,
		syncEntityBudgets,
		syncEntityRateLimits,
		syncEntityAuthKeys,
		syncEntityUsers,
		syncEntityVirtualModels,
		syncEntityPricingOverrides,
		syncEntityTagging,
		syncEntityGuardrails,
		syncEntityWorkflows,
		syncEntityMCPServers,
	}, application.clusterSync.Entities())
}

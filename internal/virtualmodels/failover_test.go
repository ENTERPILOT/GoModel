package virtualmodels

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

// failoverChain resolves source like the request path does and returns the
// qualified failover legs the gateway would sweep after the primary fails.
func failoverChain(t *testing.T, svc *Service, source string) (primary string, chain []string) {
	t.Helper()
	requested := core.NewRequestedModelSelector(source, "")
	resolved, applied, err := svc.ResolveModel(requested)
	require.NoError(t, err)

	resolution := &core.RequestModelResolution{Requested: requested, ResolvedSelector: resolved, AliasApplied: applied}
	for _, selector := range svc.ResolveFailovers(resolution, core.OperationChatCompletions) {
		chain = append(chain, selector.QualifiedModel())
	}
	return resolved.QualifiedModel(), chain
}

func TestFailover_StrategyAlwaysPicksFirstAvailableTarget(t *testing.T) {
	t.Parallel()
	catalog := balancingCatalog()
	catalog.stale = map[string]bool{"openai/gpt-4o": true}
	svc, err := NewService(newSQLVMStore(t), catalog, true)
	require.NoError(t, err)

	upsertRedirect(t, svc, "primary-first", StrategyFailover, "openai/gpt-4o", "anthropic/claude", "groq/llama")

	// The primary is unavailable, so the next leg serves every request — no
	// rotation, and the remaining legs form the chain.
	for range 3 {
		primary, chain := failoverChain(t, svc, "primary-first")
		require.Equal(t, "anthropic/claude", primary)
		require.Equal(t, []string{"groq/llama"}, chain, "chain = %v, want [groq/llama]", chain)
	}
	// Failover never pins sessions: the primary is always retried first.
	got := resolveSession(t, svc, "primary-first", "sess-a")
	require.Equal(t, "anthropic/claude", got)
}

func TestFailover_EveryStrategyExposesRemainingTargetsAsChain(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertRedirect(t, svc, "smart", StrategyRoundRobin, "openai/gpt-4o", "anthropic/claude", "groq/llama")

	primary, chain := failoverChain(t, svc, "smart")
	require.Equal(t, "openai/gpt-4o", primary)
	require.Equal(t, []string{"anthropic/claude", "groq/llama"}, chain, "chain = %v, want the other targets in declared order", chain)

	primary, chain = failoverChain(t, svc, "smart")
	require.Equal(t, "anthropic/claude", primary)
	require.Equal(t, []string{"openai/gpt-4o", "groq/llama"}, chain, "second resolution = %q, chain %v; want anthropic/claude with the others as chain", primary, chain)
}

func TestFailover_ChainDescendsChainedVirtualModels(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertRedirect(t, svc, "cheap", StrategyRoundRobin, "groq/llama", "local/mistral")
	upsertRedirect(t, svc, "resilient", StrategyFailover, "openai/gpt-4o", "cheap")

	primary, chain := failoverChain(t, svc, "resilient")
	require.Equal(t, "openai/gpt-4o", primary)
	require.Equal(t, []string{"groq/llama", "local/mistral"}, chain, "chain = %v, want every concrete model behind the chained leg", chain)
}

func TestFailover_NoChainWithoutRedirectOrForSingleTarget(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertRedirect(t, svc, "alias", "", "openai/gpt-4o")
	_, chain := failoverChain(t, svc, "alias")
	require.Empty(t, chain)
	_, chain = failoverChain(t, svc, "openai/gpt-4o")
	require.Empty(t, chain)
	got := svc.ResolveFailovers(nil, core.OperationChatCompletions)
	require.Nil(t, got)
}

// A redirect may list its own source as a target: it shadows that concrete
// model and adds a failover chain to it, which is how a legacy failover rule
// on a real model is expressed.
func TestFailover_SelfTargetShadowsConcreteModel(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	ctx := context.Background()

	err := svc.Upsert(ctx, VirtualModel{Source: "openai/gpt-4o", Targets: []Target{{Model: "openai/gpt-4o"}}, Enabled: true})
	require.Error(t, err)
	require.True(t, IsValidationError(err))

	upsertRedirect(t, svc, "openai/gpt-4o", StrategyFailover, "openai/gpt-4o", "anthropic/claude")
	primary, chain := failoverChain(t, svc, "openai/gpt-4o")
	require.Equal(t, "openai/gpt-4o", primary)
	require.Equal(t, []string{"anthropic/claude"}, chain, "resolved %q with chain %v; want the shadowed model then anthropic/claude", primary, chain)
	require.True(t, svc.Supports("openai/gpt-4o"))
	// The self target is not a chain hop, so it can be deleted like any redirect.
	err = svc.Delete(ctx, "openai/gpt-4o")
	require.NoError(t, err)
}

// A request that names its provider explicitly bypasses redirects, but keeps
// the chain of a redirect that shadows exactly that model with itself as a
// target — a migrated legacy rule on a provider model. A redirect that
// replaces the model is bypassed entirely.
func TestFailover_ExplicitProviderRequestKeepsShadowingChain(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	explicitChain := func() (string, []string) {
		requested := core.NewRequestedModelSelector("gpt-4o", "openai")
		resolved, applied, err := svc.ResolveModel(requested)
		require.NoError(t, err)
		require.False(t, applied, "ResolveModel(explicit) = %v, %v, %v; want the concrete model untouched", resolved, applied, err)

		resolution := &core.RequestModelResolution{Requested: requested, ResolvedSelector: resolved}
		var chain []string
		for _, selector := range svc.ResolveFailovers(resolution, core.OperationChatCompletions) {
			chain = append(chain, selector.QualifiedModel())
		}
		return resolved.QualifiedModel(), chain
	}

	upsertRedirect(t, svc, "openai/gpt-4o", StrategyFailover, "openai/gpt-4o", "anthropic/claude")
	primary, chain := explicitChain()
	require.Equal(t, "openai/gpt-4o", primary)
	require.Equal(t, []string{"anthropic/claude"}, chain, "resolved %q with chain %v; want the concrete model backed by its shadowing redirect", primary, chain)

	upsertRedirect(t, svc, "openai/gpt-4o", StrategyFailover, "anthropic/claude", "groq/llama")
	_, chain = explicitChain()
	require.Empty(t, chain)
}

func TestFailoverConfigModels_TranslatesLegacyRules(t *testing.T) {
	t.Parallel()
	cfg := config.FailoverConfig{
		Manual: map[string][]string{
			" gpt-4o ":        {"azure/gpt-4o", " gemini/gemini-2.5-pro "},
			"claude-sonnet-4": {"openai/gpt-5-mini"},
			"declared":        {"groq/llama"},
			"empty":           {},
			"self-only":       {"self-only", " "},
		},
		Disabled: map[string]bool{"claude-sonnet-4": true},
	}
	declared := []VirtualModel{{Source: "declared", Targets: []Target{{Model: "openai/gpt-4o"}}}}

	models := FailoverConfigModels(cfg, declared, nil)
	require.Len(t, models, 1)

	vm := models[0]
	require.Equal(t, "gpt-4o", vm.Source)
	require.Equal(t, StrategyFailover, vm.Strategy)
	require.True(t, vm.Managed)
	require.True(t, vm.Enabled, "translated model = %+v", vm)

	got := make([]string, 0, len(vm.Targets))
	for _, target := range vm.Targets {
		got = append(got, target.Model)
	}
	require.Equal(t, []string{"gpt-4o", "azure/gpt-4o", "gemini/gemini-2.5-pro"}, got, "targets = %v, want the primary first then the fallbacks in order", got)
	require.Nil(t, FailoverConfigModels(config.FailoverConfig{}, nil, nil))
}

func TestNew_MigratesLegacyFailoverRulesIntoVirtualModels(t *testing.T) {
	ctx := context.Background()
	conn := newSQLiteStorage(t)
	db := conn.DB()

	// A virtual model that predates the upgrade and collides with a rule.
	first, err := New(ctx, &config.Config{}, conn, balancingCatalog(), nil)
	require.NoError(t, err)
	err = first.Service.Upsert(ctx, VirtualModel{Source: "taken", Targets: []Target{{Model: "openai/gpt-4o"}}, Enabled: true})
	require.NoError(t, err)

	_ = first.Close()

	for _, stmt := range []string{
		`CREATE TABLE failover_rules (primary_model TEXT PRIMARY KEY, fallback_models TEXT NOT NULL DEFAULT '[]', enabled INTEGER NOT NULL DEFAULT 1, managed_source TEXT NOT NULL DEFAULT 'dashboard', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`INSERT INTO failover_rules VALUES ('openai/gpt-4o', '["groq/llama","anthropic/claude"]', 1, 'dashboard', 0, 0)`,
		`INSERT INTO failover_rules VALUES ('disabled', '["groq/llama"]', 0, 'dashboard', 0, 0)`,
		`INSERT INTO failover_rules VALUES ('from-config', '["groq/llama"]', 1, 'config', 0, 0)`,
		`INSERT INTO failover_rules VALUES ('self-only', '["self-only"]', 1, 'dashboard', 0, 0)`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err)
	}

	result, err := New(ctx, &config.Config{}, conn, balancingCatalog(), nil)
	require.NoError(t, err)

	defer result.Close()

	migrated, ok := result.Service.Get("openai/gpt-4o")
	require.True(t, ok)
	require.Equal(t, StrategyFailover, migrated.Strategy)
	require.False(t, migrated.Managed, "migrated rule = %+v, %v; want a store-backed failover redirect", migrated, ok)

	primary, chain := failoverChain(t, result.Service, "openai/gpt-4o")
	require.Equal(t, "openai/gpt-4o", primary)
	require.Equal(t, []string{"groq/llama", "anthropic/claude"}, chain, "resolved %q with chain %v; want the shadowed model and its fallbacks", primary, chain)

	for _, source := range []string{"disabled", "from-config", "self-only"} {
		_, ok := result.Service.Get(source)
		require.False(t, ok, "rule %q must not be migrated", source)
	}
	taken, _ := result.Service.Get("taken")
	require.NotNil(t, taken)
	require.NotEqual(t, StrategyFailover, taken.Strategy)

	// The legacy store is dropped, so a restart does not re-import.
	var count int
	require.Error(t, db.QueryRow(`SELECT COUNT(*) FROM failover_rules`).Scan(&count), "failover_rules still exists with %d rows, want it dropped", count)
}

// A dashboard mapping whose primary is listed in disabled_models used to be
// switched off by that setting at request time; it converts as a disabled
// virtual model so the fallbacks are kept but stay inactive.
func TestNew_DisabledModelsMigrateAsDisabledVirtualModels(t *testing.T) {
	ctx := context.Background()
	conn := newSQLiteStorage(t)
	_, err := conn.DB().Exec(`CREATE TABLE failover_rules (primary_model TEXT PRIMARY KEY, fallback_models TEXT NOT NULL DEFAULT '[]', enabled INTEGER NOT NULL DEFAULT 1, managed_source TEXT NOT NULL DEFAULT 'dashboard', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL); INSERT INTO failover_rules VALUES ('openai/gpt-4o', '["anthropic/claude"]', 1, 'dashboard', 0, 0)`)
	require.NoError(t, err)

	cfg := &config.Config{Failover: config.FailoverConfig{Disabled: map[string]bool{"openai/gpt-4o": true}}}
	result, err := New(ctx, cfg, conn, balancingCatalog(), nil)
	require.NoError(t, err)

	defer result.Close()

	migrated, ok := result.Service.Get("openai/gpt-4o")
	require.True(t, ok)
	require.False(t, migrated.Enabled)
	require.Equal(t, StrategyFailover, migrated.Strategy)
	require.Len(t, migrated.Targets, 2, "migrated rule = %+v, %v; want a disabled failover redirect keeping its fallbacks", migrated, ok)
	primary, chain := failoverChain(t, result.Service, "openai/gpt-4o")
	require.Equal(t, "openai/gpt-4o", primary)
	require.Empty(t, chain)
	require.Error(t, conn.DB().QueryRow(`SELECT COUNT(*) FROM failover_rules`).Scan(new(int)))
}

// A start that stopped after writing the converted virtual model but before
// removing its legacy row must finish the conversion on the next start, not
// report its own conversion as a collision.
func TestNew_FinishesInterruptedLegacyFailoverMigration(t *testing.T) {
	ctx := context.Background()
	conn := newSQLiteStorage(t)
	db := conn.DB()

	first, err := New(ctx, &config.Config{}, conn, balancingCatalog(), nil)
	require.NoError(t, err)

	converted, _ := failoverModel("openai/gpt-4o", []string{"groq/llama"}, false)
	err = first.Service.Upsert(ctx, converted)
	require.NoError(t, err)

	_ = first.Close()
	for _, stmt := range []string{
		`CREATE TABLE failover_rules (primary_model TEXT PRIMARY KEY, fallback_models TEXT NOT NULL DEFAULT '[]', enabled INTEGER NOT NULL DEFAULT 1, managed_source TEXT NOT NULL DEFAULT 'dashboard', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`INSERT INTO failover_rules VALUES ('openai/gpt-4o', '["groq/llama"]', 1, 'dashboard', 0, 0)`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err)
	}

	result, err := New(ctx, &config.Config{}, conn, balancingCatalog(), nil)
	require.NoError(t, err)

	defer result.Close()
	vm, ok := result.Service.Get("openai/gpt-4o")
	require.True(t, ok)
	require.Equal(t, StrategyFailover, vm.Strategy, "converted model = %+v, %v", vm, ok)

	var count int
	require.Error(t, db.QueryRow(`SELECT COUNT(*) FROM failover_rules`).Scan(&count), "failover_rules still exists with %d rows, want it dropped", count)

	_ = result.Close()
	// The same marker on a model with different targets is the operator's
	// own work, so the rule is retained as a collision instead of dropped.
	_, err = db.Exec(`CREATE TABLE failover_rules (primary_model TEXT PRIMARY KEY, fallback_models TEXT NOT NULL DEFAULT '[]', enabled INTEGER NOT NULL DEFAULT 1, managed_source TEXT NOT NULL DEFAULT 'dashboard', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL); INSERT INTO failover_rules VALUES ('openai/gpt-4o', '["anthropic/claude"]', 1, 'dashboard', 0, 0)`)
	require.NoError(t, err)

	again, err := New(ctx, &config.Config{}, conn, balancingCatalog(), nil)
	require.NoError(t, err)

	defer again.Close()
	vm, _ = again.Service.Get("openai/gpt-4o")
	require.NotNil(t, vm)
	require.Equal(t, "groq/llama", vm.Targets[1].Model)
	err = db.QueryRow(`SELECT COUNT(*) FROM failover_rules`).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

// A legacy rule colliding with an existing virtual model is neither merged nor
// discarded: the store stays until the operator resolves it, and the migrated
// rows are not duplicated on the next start.
func TestNew_KeepsLegacyFailoverStoreWhileARuleCollides(t *testing.T) {
	ctx := context.Background()
	conn := newSQLiteStorage(t)
	db := conn.DB()

	first, err := New(ctx, &config.Config{}, conn, balancingCatalog(), nil)
	require.NoError(t, err)
	err = first.Service.Upsert(ctx, VirtualModel{Source: "taken", Targets: []Target{{Model: "openai/gpt-4o"}}, Enabled: true})
	require.NoError(t, err)

	_ = first.Close()
	for _, stmt := range []string{
		`CREATE TABLE failover_rules (primary_model TEXT PRIMARY KEY, fallback_models TEXT NOT NULL DEFAULT '[]', enabled INTEGER NOT NULL DEFAULT 1, managed_source TEXT NOT NULL DEFAULT 'dashboard', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`INSERT INTO failover_rules VALUES ('openai/gpt-4o', '["groq/llama"]', 1, 'dashboard', 0, 0)`,
		`INSERT INTO failover_rules VALUES ('taken', '["groq/llama"]', 1, 'dashboard', 0, 0)`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err)
	}

	for range 2 {
		result, err := New(ctx, &config.Config{}, conn, balancingCatalog(), nil)
		require.NoError(t, err)
		migrated, ok := result.Service.Get("openai/gpt-4o")
		require.True(t, ok)
		require.Equal(t, StrategyFailover, migrated.Strategy, "migrated rule = %+v, %v", migrated, ok)
		taken, _ := result.Service.Get("taken")
		require.NotNil(t, taken)
		require.NotEqual(t, StrategyFailover, taken.Strategy)

		_ = result.Close()
	}
	// Only the colliding row remains, so resolving it lets the next start
	// finish the migration and drop the store.
	var remaining string
	err = db.QueryRow(`SELECT group_concat(primary_model) FROM failover_rules`).Scan(&remaining)
	require.NoError(t, err)
	require.Equal(t, "taken", remaining)
	_, err = db.Exec(`DELETE FROM failover_rules WHERE primary_model = 'taken'`)
	require.NoError(t, err)

	result, err := New(ctx, &config.Config{}, conn, balancingCatalog(), nil)
	require.NoError(t, err)

	_ = result.Close()
	var count int
	require.Error(t, db.QueryRow(`SELECT COUNT(*) FROM failover_rules`).Scan(&count), "failover_rules still exists with %d rows, want it dropped", count)
}

// A legacy rule whose fallback names a stored redirect that routes back to the
// rule's primary would convert into a chain cycle. The migration used to write
// it through the raw store (which does not validate), then drop the legacy
// rows — leaving a virtual_models set every later start refuses to load. Such
// a rule must instead stay in the legacy store, like a collision, until the
// operator resolves it.
func TestNew_KeepsLegacyFailoverRuleThatWouldFormAChainCycle(t *testing.T) {
	ctx := context.Background()
	conn := newSQLiteStorage(t)
	db := conn.DB()

	first, err := New(ctx, &config.Config{}, conn, balancingCatalog(), nil)
	require.NoError(t, err)
	// A replacing alias: requests for anthropic/claude go to groq/llama.
	err = first.Service.Upsert(ctx, VirtualModel{Source: "anthropic/claude", Targets: []Target{{Model: "groq/llama"}}, Enabled: true})
	require.NoError(t, err)

	_ = first.Close()
	for _, stmt := range []string{
		`CREATE TABLE failover_rules (primary_model TEXT PRIMARY KEY, fallback_models TEXT NOT NULL DEFAULT '[]', enabled INTEGER NOT NULL DEFAULT 1, managed_source TEXT NOT NULL DEFAULT 'dashboard', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		// Falls back to anthropic/claude, which the alias routes right back.
		`INSERT INTO failover_rules VALUES ('groq/llama', '["anthropic/claude"]', 1, 'dashboard', 0, 0)`,
		`INSERT INTO failover_rules VALUES ('openai/gpt-4o', '["local/mistral"]', 1, 'dashboard', 0, 0)`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err)
	}

	for range 2 {
		result, err := New(ctx, &config.Config{}, conn, balancingCatalog(), nil)
		require.NoError(t, err)
		migrated, ok := result.Service.Get("openai/gpt-4o")
		require.True(t, ok)
		require.Equal(t, StrategyFailover, migrated.Strategy, "convertible rule = %+v, %v; want it migrated alongside the cyclic one", migrated, ok)
		vm, ok := result.Service.Get("groq/llama")
		require.False(t, ok, "cyclic rule must not be converted, got %+v", vm)

		_ = result.Close()
	}
	var remaining string
	err = db.QueryRow(`SELECT group_concat(primary_model) FROM failover_rules`).Scan(&remaining)
	require.NoError(t, err)
	require.Equal(t, "groq/llama", remaining)

	// Removing the alias resolves the cycle: the next start finishes the
	// migration and drops the store.
	resolve, err := New(ctx, &config.Config{}, conn, balancingCatalog(), nil)
	require.NoError(t, err)
	err = resolve.Service.Delete(ctx, "anthropic/claude")
	require.NoError(t, err)

	_ = resolve.Close()
	result, err := New(ctx, &config.Config{}, conn, balancingCatalog(), nil)
	require.NoError(t, err)

	defer result.Close()
	vm, ok := result.Service.Get("groq/llama")
	require.True(t, ok)
	require.Equal(t, StrategyFailover, vm.Strategy, "resolved rule = %+v, %v; want it migrated", vm, ok)
	require.Error(t, db.QueryRow(`SELECT COUNT(*) FROM failover_rules`).Scan(new(int)))
}

// The first refresh also translates the deprecated failover rules
// configuration into managed redirects, so the conversion check must include
// them: a cycle can pass through a generated rule when replacing aliases sit
// between it and the legacy rule (shadow-to-shadow references alone do not
// chain).
func TestNew_KeepsLegacyFailoverRuleThatCyclesThroughGeneratedConfigRule(t *testing.T) {
	ctx := context.Background()
	conn := newSQLiteStorage(t)
	db := conn.DB()
	for _, stmt := range []string{
		`CREATE TABLE failover_rules (primary_model TEXT PRIMARY KEY, fallback_models TEXT NOT NULL DEFAULT '[]', enabled INTEGER NOT NULL DEFAULT 1, managed_source TEXT NOT NULL DEFAULT 'dashboard', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`INSERT INTO failover_rules VALUES ('openai/gpt-4o', '["alias-one"]', 1, 'dashboard', 0, 0)`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err)
	}
	// migrated openai/gpt-4o -> alias-one -> cfg-primary (generated rule) ->
	// alias-two -> openai/gpt-4o: every edge chains, so this is a cycle.
	cfg := &config.Config{
		VirtualModels: []config.VirtualModelConfig{
			{Source: "alias-one", Targets: []config.VirtualModelTargetConfig{{Model: "cfg-primary"}}},
			{Source: "alias-two", Targets: []config.VirtualModelTargetConfig{{Model: "openai/gpt-4o"}}},
		},
		Failover: config.FailoverConfig{Manual: map[string][]string{"cfg-primary": {"alias-two"}}},
	}

	for range 2 {
		result, err := New(ctx, cfg, conn, balancingCatalog(), nil)
		require.NoError(t, err)

		if vm, ok := result.Service.Get("openai/gpt-4o"); ok {
			require.True(t, vm.Managed, "cyclic rule must not be converted, got %+v", vm)
		}
		_ = result.Close()
	}
	var remaining string
	err := db.QueryRow(`SELECT group_concat(primary_model) FROM failover_rules`).Scan(&remaining)
	require.NoError(t, err)
	require.Equal(t, "openai/gpt-4o", remaining)
}

// A database whose virtual_models rows no longer validate — the state the old
// migration left behind after committing a cycle and dropping the legacy store
// — must fail startup with guidance that points at the store, since the admin
// API is unreachable while the server is down.
func TestNew_InvalidStoredVirtualModelsFailWithRepairGuidance(t *testing.T) {
	ctx := context.Background()
	conn := newSQLiteStorage(t)

	warm, err := New(ctx, &config.Config{}, conn, balancingCatalog(), nil)
	require.NoError(t, err)

	_ = warm.Close()
	for _, stmt := range []string{
		`INSERT INTO virtual_models (source, targets, enabled, created_at, updated_at) VALUES ('anthropic/claude', '[{"model":"groq/llama"}]', TRUE, 0, 0)`,
		`INSERT INTO virtual_models (source, targets, strategy, description, enabled, created_at, updated_at) VALUES ('groq/llama', '[{"model":"groq/llama"},{"model":"anthropic/claude"}]', 'failover', 'Migrated from failover rules', TRUE, 0, 0)`,
	} {
		_, err := conn.DB().Exec(stmt)
		require.NoError(t, err)
	}

	_, err = New(ctx, &config.Config{}, conn, balancingCatalog(), nil)
	require.ErrorContains(t, err, "forms a cycle")
	require.ErrorContains(t, err, "stored virtual_models entries")
}

// The declarative config models are overlaid on the store after the migration
// runs, so the conversion check must see them too: a config-declared alias
// routing a legacy rule's fallback back to its primary forms the same cycle a
// stored alias does, and committing it would destroy the legacy rows and fail
// every start until the config changes.
func TestNew_KeepsLegacyFailoverRuleThatCyclesThroughConfigModel(t *testing.T) {
	ctx := context.Background()
	conn := newSQLiteStorage(t)
	db := conn.DB()
	for _, stmt := range []string{
		`CREATE TABLE failover_rules (primary_model TEXT PRIMARY KEY, fallback_models TEXT NOT NULL DEFAULT '[]', enabled INTEGER NOT NULL DEFAULT 1, managed_source TEXT NOT NULL DEFAULT 'dashboard', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`INSERT INTO failover_rules VALUES ('groq/llama', '["anthropic/claude"]', 1, 'dashboard', 0, 0)`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err)
	}
	cfg := &config.Config{VirtualModels: []config.VirtualModelConfig{
		// A replacing alias, declared in config rather than stored.
		{Source: "anthropic/claude", Targets: []config.VirtualModelTargetConfig{{Model: "groq/llama"}}},
	}}

	for range 2 {
		result, err := New(ctx, cfg, conn, balancingCatalog(), nil)
		require.NoError(t, err)

		if vm, ok := result.Service.Get("groq/llama"); ok {
			require.True(t, vm.Managed, "cyclic rule must not be converted, got %+v", vm)
		}
		_ = result.Close()
	}
	var remaining string
	err := db.QueryRow(`SELECT group_concat(primary_model) FROM failover_rules`).Scan(&remaining)
	require.NoError(t, err)
	require.Equal(t, "groq/llama", remaining)

	// Dropping the alias from config resolves the cycle: the next start
	// finishes the migration and drops the store.
	result, err := New(ctx, &config.Config{}, conn, balancingCatalog(), nil)
	require.NoError(t, err)

	defer result.Close()
	vm, ok := result.Service.Get("groq/llama")
	require.True(t, ok)
	require.Equal(t, StrategyFailover, vm.Strategy, "resolved rule = %+v, %v; want it migrated", vm, ok)
	require.Error(t, db.QueryRow(`SELECT COUNT(*) FROM failover_rules`).Scan(new(int)))
}

func TestFailover_FlagSwitchesTheChainOffPerRedirect(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	ctx := context.Background()
	off := false
	upsert := func(source, strategy string, failover *bool) {
		t.Helper()
		err := svc.Upsert(ctx, VirtualModel{
			Source:   source,
			Strategy: strategy,
			Failover: failover,
			Targets:  []Target{{Model: "openai/gpt-4o"}, {Model: "anthropic/claude"}},
			Enabled:  true,
		})
		require.NoError(t, err)
	}
	upsert("default-on", StrategyRoundRobin, nil)
	upsert("switched-off", StrategyRoundRobin, &off)
	upsert("priority", StrategyFailover, &off)
	_, chain := failoverChain(t, svc, "default-on")
	require.Len(t, chain, 1)
	_, chain = failoverChain(t, svc, "switched-off")
	require.Empty(t, chain)
	// The failover strategy is a priority list: the flag cannot switch it off.
	_, chain = failoverChain(t, svc, "priority")
	require.Len(t, chain, 1)

	// The flag survives the store round trip and is reported to the admin UI.
	for _, view := range svc.ListViews() {
		if view.Source != "switched-off" {
			continue
		}
		require.NotNil(t, view.Failover)
		require.False(t, *view.Failover)
	}
}

// A failover_rules table from before its columns were renamed (source /
// targets / description) is read as-is; the rename used to run in the store
// constructor that no longer exists.
func TestNew_MigratesPreRenameLegacyFailoverTable(t *testing.T) {
	ctx := context.Background()
	conn := newSQLiteStorage(t)
	db := conn.DB()
	for _, stmt := range []string{
		`CREATE TABLE failover_rules (source TEXT PRIMARY KEY, targets TEXT NOT NULL DEFAULT '[]', description TEXT NOT NULL DEFAULT '')`,
		`INSERT INTO failover_rules VALUES (' openai/gpt-4o ', '["groq/llama"]', 'old note')`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err)
	}
	result, err := New(ctx, &config.Config{}, conn, balancingCatalog(), nil)
	require.NoError(t, err)

	defer result.Close()
	migrated, ok := result.Service.Get("openai/gpt-4o")
	require.True(t, ok)
	require.Equal(t, StrategyFailover, migrated.Strategy)
	require.Len(t, migrated.Targets, 2)
	require.Equal(t, "groq/llama", migrated.Targets[1].Model, "migrated = %+v, %v; want failover redirect over [openai/gpt-4o groq/llama]", migrated, ok)

	var count int
	require.Error(t, db.QueryRow(`SELECT COUNT(*) FROM failover_rules`).Scan(&count), "failover_rules still exists with %d rows, want it dropped", count)
}

// A legacy rule whose primary already has a plain per-model policy (slowdown,
// description) is merged into it: the policy becomes the failover redirect
// and keeps its settings. A path-scoped policy is left for the operator.
func TestNew_MergesLegacyFailoverRuleIntoPlainPolicy(t *testing.T) {
	ctx := context.Background()
	conn := newSQLiteStorage(t)
	db := conn.DB()

	first, err := New(ctx, &config.Config{}, conn, balancingCatalog(), nil)
	require.NoError(t, err)

	for _, policy := range []VirtualModel{
		{Source: "openai/gpt-4o", Slowdown: new(0.5), Description: "my note", Enabled: true},
		{Source: "anthropic/claude", UserPaths: []string{"/team"}, Enabled: true},
	} {
		err := first.Service.Upsert(ctx, policy)
		require.NoError(t, err)
	}
	_ = first.Close()
	for _, stmt := range []string{
		`CREATE TABLE failover_rules (primary_model TEXT PRIMARY KEY, fallback_models TEXT NOT NULL DEFAULT '[]', enabled INTEGER NOT NULL DEFAULT 1, managed_source TEXT NOT NULL DEFAULT 'dashboard', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`INSERT INTO failover_rules VALUES ('openai/gpt-4o', '["groq/llama"]', 1, 'dashboard', 0, 0)`,
		`INSERT INTO failover_rules VALUES ('anthropic/claude', '["groq/llama"]', 1, 'dashboard', 0, 0)`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err)
	}

	result, err := New(ctx, &config.Config{}, conn, balancingCatalog(), nil)
	require.NoError(t, err)

	defer result.Close()
	merged, ok := result.Service.Get("openai/gpt-4o")
	require.True(t, ok)
	require.Equal(t, StrategyFailover, merged.Strategy)
	require.Len(t, merged.Targets, 2, "merged = %+v, %v; want failover redirect", merged, ok)
	require.Equal(t, "my note", merged.Description)
	require.NotNil(t, merged.Slowdown)
	require.Equal(t, 0.5, *merged.Slowdown)
	require.True(t, merged.Enabled, "merged policy settings lost: %+v", merged)
	scoped, _ := result.Service.Get("anthropic/claude")
	require.NotNil(t, scoped)
	require.False(t, scoped.IsRedirect())

	var remaining string
	err = db.QueryRow(`SELECT group_concat(primary_model) FROM failover_rules`).Scan(&remaining)
	require.NoError(t, err)
	require.Equal(t, "anthropic/claude", remaining)
}

// A deprecated failover.rules entry never overlays a stored virtual model of
// the same source, so an upgrade cannot silently change its routing.
func TestNew_ConfigFailoverRuleDoesNotHideStoredVirtualModel(t *testing.T) {
	ctx := context.Background()
	conn := newSQLiteStorage(t)

	first, err := New(ctx, &config.Config{}, conn, balancingCatalog(), nil)
	require.NoError(t, err)
	err = first.Service.Upsert(ctx, VirtualModel{Source: "openai/gpt-4o", Targets: []Target{{Model: "anthropic/claude"}}, Enabled: true})
	require.NoError(t, err)

	_ = first.Close()

	cfg := &config.Config{}
	cfg.Failover.Manual = map[string][]string{"openai/gpt-4o": {"groq/llama"}, "groq/llama": {"local/mistral"}}
	result, err := New(ctx, cfg, conn, balancingCatalog(), nil)
	require.NoError(t, err)

	defer result.Close()
	stored, ok := result.Service.Get("openai/gpt-4o")
	require.True(t, ok)
	require.False(t, stored.Managed)
	require.NotEqual(t, StrategyFailover, stored.Strategy)
	require.Equal(t, "anthropic/claude", stored.Targets[0].Model, "stored virtual model overlaid by the config rule: %+v", stored)
	translated, ok := result.Service.Get("groq/llama")
	require.True(t, ok)
	require.True(t, translated.Managed)
	require.Equal(t, StrategyFailover, translated.Strategy, "non-colliding rule not translated: %+v, %v", translated, ok)
}

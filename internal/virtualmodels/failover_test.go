package virtualmodels

import (
	"context"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

// failoverChain resolves source like the request path does and returns the
// qualified failover legs the gateway would sweep after the primary fails.
func failoverChain(t *testing.T, svc *Service, source string) (primary string, chain []string) {
	t.Helper()
	requested := core.NewRequestedModelSelector(source, "")
	resolved, applied, err := svc.ResolveModel(requested)
	if err != nil {
		t.Fatalf("ResolveModel(%s) error = %v", source, err)
	}
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
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	upsertRedirect(t, svc, "primary-first", StrategyFailover, "openai/gpt-4o", "anthropic/claude", "groq/llama")

	// The primary is unavailable, so the next leg serves every request — no
	// rotation, and the remaining legs form the chain.
	for range 3 {
		primary, chain := failoverChain(t, svc, "primary-first")
		if primary != "anthropic/claude" {
			t.Fatalf("resolved %q, want anthropic/claude while the primary is unavailable", primary)
		}
		if strings.Join(chain, ",") != "groq/llama" {
			t.Fatalf("chain = %v, want [groq/llama]", chain)
		}
	}
	// Failover never pins sessions: the primary is always retried first.
	if got := resolveSession(t, svc, "primary-first", "sess-a"); got != "anthropic/claude" {
		t.Fatalf("session resolved %q, want anthropic/claude", got)
	}
}

func TestFailover_EveryStrategyExposesRemainingTargetsAsChain(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertRedirect(t, svc, "smart", StrategyRoundRobin, "openai/gpt-4o", "anthropic/claude", "groq/llama")

	primary, chain := failoverChain(t, svc, "smart")
	if primary != "openai/gpt-4o" {
		t.Fatalf("first resolution = %q, want openai/gpt-4o", primary)
	}
	if strings.Join(chain, ",") != "anthropic/claude,groq/llama" {
		t.Fatalf("chain = %v, want the other targets in declared order", chain)
	}
	primary, chain = failoverChain(t, svc, "smart")
	if primary != "anthropic/claude" || strings.Join(chain, ",") != "openai/gpt-4o,groq/llama" {
		t.Fatalf("second resolution = %q, chain %v; want anthropic/claude with the others as chain", primary, chain)
	}
}

func TestFailover_ChainDescendsChainedVirtualModels(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertRedirect(t, svc, "cheap", StrategyRoundRobin, "groq/llama", "local/mistral")
	upsertRedirect(t, svc, "resilient", StrategyFailover, "openai/gpt-4o", "cheap")

	primary, chain := failoverChain(t, svc, "resilient")
	if primary != "openai/gpt-4o" {
		t.Fatalf("resolved %q, want openai/gpt-4o", primary)
	}
	if strings.Join(chain, ",") != "groq/llama,local/mistral" {
		t.Fatalf("chain = %v, want every concrete model behind the chained leg", chain)
	}
}

func TestFailover_NoChainWithoutRedirectOrForSingleTarget(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertRedirect(t, svc, "alias", "", "openai/gpt-4o")

	if _, chain := failoverChain(t, svc, "alias"); len(chain) != 0 {
		t.Fatalf("single-target alias chain = %v, want none", chain)
	}
	if _, chain := failoverChain(t, svc, "openai/gpt-4o"); len(chain) != 0 {
		t.Fatalf("concrete model chain = %v, want none", chain)
	}
	if got := svc.ResolveFailovers(nil, core.OperationChatCompletions); got != nil {
		t.Fatalf("ResolveFailovers(nil) = %v, want nil", got)
	}
}

// A redirect may list its own source as a target: it shadows that concrete
// model and adds a failover chain to it, which is how a legacy failover rule
// on a real model is expressed.
func TestFailover_SelfTargetShadowsConcreteModel(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	ctx := context.Background()

	err := svc.Upsert(ctx, VirtualModel{Source: "openai/gpt-4o", Targets: []Target{{Model: "openai/gpt-4o"}}, Enabled: true})
	if err == nil || !IsValidationError(err) {
		t.Fatalf("Upsert(sole self target) error = %v, want validation error", err)
	}

	upsertRedirect(t, svc, "openai/gpt-4o", StrategyFailover, "openai/gpt-4o", "anthropic/claude")
	primary, chain := failoverChain(t, svc, "openai/gpt-4o")
	if primary != "openai/gpt-4o" || strings.Join(chain, ",") != "anthropic/claude" {
		t.Fatalf("resolved %q with chain %v; want the shadowed model then anthropic/claude", primary, chain)
	}
	if !svc.Supports("openai/gpt-4o") {
		t.Fatalf("Supports(shadowing redirect) = false, want true")
	}
	// The self target is not a chain hop, so it can be deleted like any redirect.
	if err := svc.Delete(ctx, "openai/gpt-4o"); err != nil {
		t.Fatalf("Delete(shadowing redirect) error = %v", err)
	}
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

	models := FailoverConfigModels(cfg, declared)
	if len(models) != 1 {
		t.Fatalf("FailoverConfigModels() = %+v, want only gpt-4o", models)
	}
	vm := models[0]
	if vm.Source != "gpt-4o" || vm.Strategy != StrategyFailover || !vm.Managed || !vm.Enabled {
		t.Fatalf("translated model = %+v", vm)
	}
	got := make([]string, 0, len(vm.Targets))
	for _, target := range vm.Targets {
		got = append(got, target.Model)
	}
	if strings.Join(got, ",") != "gpt-4o,azure/gpt-4o,gemini/gemini-2.5-pro" {
		t.Fatalf("targets = %v, want the primary first then the fallbacks in order", got)
	}
	if FailoverConfigModels(config.FailoverConfig{}, nil) != nil {
		t.Fatalf("FailoverConfigModels(empty) should be nil")
	}
}

func TestNew_MigratesLegacyFailoverRulesIntoVirtualModels(t *testing.T) {
	ctx := context.Background()
	conn := newSQLiteStorage(t)
	db := conn.DB()

	// A virtual model that predates the upgrade and collides with a rule.
	first, err := New(ctx, &config.Config{}, conn, balancingCatalog(), nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := first.Service.Upsert(ctx, VirtualModel{Source: "taken", Targets: []Target{{Model: "openai/gpt-4o"}}, Enabled: true}); err != nil {
		t.Fatalf("Upsert(taken) error = %v", err)
	}
	_ = first.Close()

	for _, stmt := range []string{
		`CREATE TABLE failover_rules (primary_model TEXT PRIMARY KEY, fallback_models TEXT NOT NULL DEFAULT '[]', enabled INTEGER NOT NULL DEFAULT 1, managed_source TEXT NOT NULL DEFAULT 'dashboard', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`INSERT INTO failover_rules VALUES ('openai/gpt-4o', '["groq/llama","anthropic/claude"]', 1, 'dashboard', 0, 0)`,
		`INSERT INTO failover_rules VALUES ('disabled', '["groq/llama"]', 0, 'dashboard', 0, 0)`,
		`INSERT INTO failover_rules VALUES ('from-config', '["groq/llama"]', 1, 'config', 0, 0)`,
		`INSERT INTO failover_rules VALUES ('self-only', '["self-only"]', 1, 'dashboard', 0, 0)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	result, err := New(ctx, &config.Config{}, conn, balancingCatalog(), nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer result.Close()

	migrated, ok := result.Service.Get("openai/gpt-4o")
	if !ok || migrated.Strategy != StrategyFailover || migrated.Managed {
		t.Fatalf("migrated rule = %+v, %v; want a store-backed failover redirect", migrated, ok)
	}
	primary, chain := failoverChain(t, result.Service, "openai/gpt-4o")
	if primary != "openai/gpt-4o" || strings.Join(chain, ",") != "groq/llama,anthropic/claude" {
		t.Fatalf("resolved %q with chain %v; want the shadowed model and its fallbacks", primary, chain)
	}
	for _, source := range []string{"disabled", "from-config", "self-only"} {
		if _, ok := result.Service.Get(source); ok {
			t.Fatalf("rule %q must not be migrated", source)
		}
	}
	if taken, _ := result.Service.Get("taken"); taken == nil || taken.Strategy == StrategyFailover {
		t.Fatalf("existing virtual model must be left untouched, got %+v", taken)
	}

	// The legacy store is dropped, so a restart does not re-import.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM failover_rules`).Scan(&count); err == nil {
		t.Fatalf("failover_rules still exists with %d rows, want it dropped", count)
	}
}

// A start that stopped after writing the converted virtual model but before
// removing its legacy row must finish the conversion on the next start, not
// report its own conversion as a collision.
func TestNew_FinishesInterruptedLegacyFailoverMigration(t *testing.T) {
	ctx := context.Background()
	conn := newSQLiteStorage(t)
	db := conn.DB()

	first, err := New(ctx, &config.Config{}, conn, balancingCatalog(), nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	converted, _ := failoverModel("openai/gpt-4o", []string{"groq/llama"}, false)
	if err := first.Service.Upsert(ctx, converted); err != nil {
		t.Fatalf("Upsert(converted) error = %v", err)
	}
	_ = first.Close()
	for _, stmt := range []string{
		`CREATE TABLE failover_rules (primary_model TEXT PRIMARY KEY, fallback_models TEXT NOT NULL DEFAULT '[]', enabled INTEGER NOT NULL DEFAULT 1, managed_source TEXT NOT NULL DEFAULT 'dashboard', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`INSERT INTO failover_rules VALUES ('openai/gpt-4o', '["groq/llama"]', 1, 'dashboard', 0, 0)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	result, err := New(ctx, &config.Config{}, conn, balancingCatalog(), nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer result.Close()
	if vm, ok := result.Service.Get("openai/gpt-4o"); !ok || vm.Strategy != StrategyFailover {
		t.Fatalf("converted model = %+v, %v", vm, ok)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM failover_rules`).Scan(&count); err == nil {
		t.Fatalf("failover_rules still exists with %d rows, want it dropped", count)
	}
	_ = result.Close()

	// The same marker on a model with different targets is the operator's
	// own work, so the rule is retained as a collision instead of dropped.
	if _, err := db.Exec(`CREATE TABLE failover_rules (primary_model TEXT PRIMARY KEY, fallback_models TEXT NOT NULL DEFAULT '[]', enabled INTEGER NOT NULL DEFAULT 1, managed_source TEXT NOT NULL DEFAULT 'dashboard', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL); INSERT INTO failover_rules VALUES ('openai/gpt-4o', '["anthropic/claude"]', 1, 'dashboard', 0, 0)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	again, err := New(ctx, &config.Config{}, conn, balancingCatalog(), nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer again.Close()
	if vm, _ := again.Service.Get("openai/gpt-4o"); vm == nil || vm.Targets[1].Model != "groq/llama" {
		t.Fatalf("existing model must be untouched, got %+v", vm)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM failover_rules`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("failover_rules rows = %d, %v; want the differing rule retained", count, err)
	}
}

// A legacy rule colliding with an existing virtual model is neither merged nor
// discarded: the store stays until the operator resolves it, and the migrated
// rows are not duplicated on the next start.
func TestNew_KeepsLegacyFailoverStoreWhileARuleCollides(t *testing.T) {
	ctx := context.Background()
	conn := newSQLiteStorage(t)
	db := conn.DB()

	first, err := New(ctx, &config.Config{}, conn, balancingCatalog(), nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := first.Service.Upsert(ctx, VirtualModel{Source: "taken", Targets: []Target{{Model: "openai/gpt-4o"}}, Enabled: true}); err != nil {
		t.Fatalf("Upsert(taken) error = %v", err)
	}
	_ = first.Close()
	for _, stmt := range []string{
		`CREATE TABLE failover_rules (primary_model TEXT PRIMARY KEY, fallback_models TEXT NOT NULL DEFAULT '[]', enabled INTEGER NOT NULL DEFAULT 1, managed_source TEXT NOT NULL DEFAULT 'dashboard', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`INSERT INTO failover_rules VALUES ('openai/gpt-4o', '["groq/llama"]', 1, 'dashboard', 0, 0)`,
		`INSERT INTO failover_rules VALUES ('taken', '["groq/llama"]', 1, 'dashboard', 0, 0)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	for range 2 {
		result, err := New(ctx, &config.Config{}, conn, balancingCatalog(), nil)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		if migrated, ok := result.Service.Get("openai/gpt-4o"); !ok || migrated.Strategy != StrategyFailover {
			t.Fatalf("migrated rule = %+v, %v", migrated, ok)
		}
		if taken, _ := result.Service.Get("taken"); taken == nil || taken.Strategy == StrategyFailover {
			t.Fatalf("colliding virtual model must be left untouched, got %+v", taken)
		}
		_ = result.Close()
	}
	// Only the colliding row remains, so resolving it lets the next start
	// finish the migration and drop the store.
	var remaining string
	if err := db.QueryRow(`SELECT group_concat(primary_model) FROM failover_rules`).Scan(&remaining); err != nil || remaining != "taken" {
		t.Fatalf("failover_rules rows = %q, %v; want only the colliding row", remaining, err)
	}
	if _, err := db.Exec(`DELETE FROM failover_rules WHERE primary_model = 'taken'`); err != nil {
		t.Fatalf("resolve collision: %v", err)
	}
	result, err := New(ctx, &config.Config{}, conn, balancingCatalog(), nil)
	if err != nil {
		t.Fatalf("New() after resolving error = %v", err)
	}
	_ = result.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM failover_rules`).Scan(&count); err == nil {
		t.Fatalf("failover_rules still exists with %d rows, want it dropped", count)
	}
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
		if err != nil {
			t.Fatalf("Upsert(%s) error = %v", source, err)
		}
	}
	upsert("default-on", StrategyRoundRobin, nil)
	upsert("switched-off", StrategyRoundRobin, &off)
	upsert("priority", StrategyFailover, &off)

	if _, chain := failoverChain(t, svc, "default-on"); len(chain) != 1 {
		t.Fatalf("default-on chain = %v, want one leg", chain)
	}
	if _, chain := failoverChain(t, svc, "switched-off"); len(chain) != 0 {
		t.Fatalf("switched-off chain = %v, want none", chain)
	}
	// The failover strategy is a priority list: the flag cannot switch it off.
	if _, chain := failoverChain(t, svc, "priority"); len(chain) != 1 {
		t.Fatalf("priority chain = %v, want one leg despite failover=false", chain)
	}

	// The flag survives the store round trip and is reported to the admin UI.
	for _, view := range svc.ListViews() {
		if view.Source == "switched-off" && (view.Failover == nil || *view.Failover) {
			t.Fatalf("view.Failover = %v, want false", view.Failover)
		}
	}
}

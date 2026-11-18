package virtualmodels

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// benchService builds a service over the balancing catalog with one redirect
// of every shape the request path can take.
func benchService(b *testing.B) *Service {
	b.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	b.Cleanup(func() { db.Close() })
	sqlDB, err := sqlx.NewSQLite(db)
	if err != nil {
		b.Fatal(err)
	}
	store, err := NewSQLStore(context.Background(), sqlDB)
	if err != nil {
		b.Fatal(err)
	}
	svc, err := NewService(store, balancingCatalog(), true)
	if err != nil {
		b.Fatal(err)
	}
	three := benchTargets()
	for _, vm := range []VirtualModel{
		{Source: "alias", Targets: []Target{{Model: "openai/gpt-4o"}}},
		{Source: "flat", Strategy: StrategyRoundRobin, Targets: three},
		{Source: "cost", Strategy: StrategyCost, Targets: three},
		{Source: "priority", Strategy: StrategyFailover, Targets: three},
		{Source: "chained", Strategy: StrategyRoundRobin, Targets: []Target{{Model: "flat"}, {Model: "cost"}}},
	} {
		vm.Enabled = true
		if err := svc.Upsert(context.Background(), vm); err != nil {
			b.Fatal(err)
		}
	}
	return svc
}

func benchTargets() []Target {
	return []Target{{Model: "openai/gpt-4o"}, {Model: "anthropic/claude"}, {Model: "groq/llama"}}
}

// BenchmarkResolveModel measures one request's redirect resolution per
// redirect shape; a passthrough is a request naming a concrete model.
func BenchmarkResolveModel(b *testing.B) {
	for _, source := range []string{"openai/gpt-4o", "alias", "flat", "cost", "priority", "chained"} {
		b.Run(source, func(b *testing.B) {
			svc := benchService(b)
			sel := core.NewRequestedModelSelector(source, "")
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, _, err := svc.ResolveModel(sel); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkResolveRequestOverrides measures the per-attempt override lookup
// for a failover redirect: with nothing configured, with an effort on the
// redirect, and with an effort per target, for the first attempt and for a
// failover leg.
func BenchmarkResolveRequestOverrides(b *testing.B) {
	perTarget := []Target{
		{Model: "openai/gpt-4o", ReasoningEffort: "high"},
		{Model: "anthropic/claude", ReasoningEffort: "low"},
		{Model: "groq/llama", ReasoningEffort: "medium"},
	}
	shapes := []struct {
		name string
		vm   VirtualModel
	}{
		{name: "none"},
		{name: "redirect", vm: VirtualModel{Source: "effort", Strategy: StrategyFailover, Targets: benchTargets(), ReasoningEffort: "high", Enabled: true}},
		{name: "per_target", vm: VirtualModel{Source: "effort", Strategy: StrategyFailover, Targets: perTarget, Enabled: true}},
	}
	for _, shape := range shapes {
		svc := benchService(b)
		source := "priority"
		if shape.vm.Source != "" {
			source = shape.vm.Source
			if err := svc.Upsert(context.Background(), shape.vm); err != nil {
				b.Fatal(err)
			}
		}
		ctx := context.Background()
		requested := core.NewRequestedModelSelector(source, "")
		resolved, path, applied, err := svc.ResolveModelRouteForUserPath(ctx, requested)
		if err != nil || !applied {
			b.Fatalf("ResolveModelRouteForUserPath() = %v, %v, %v", resolved, applied, err)
		}
		resolution := &core.RequestModelResolution{Requested: requested, ResolvedSelector: resolved, AliasApplied: applied, VirtualModelPath: path}
		resolution.CacheDerivedSelectors()
		attempts := map[string]core.ModelSelector{
			"first":    resolved,
			"failover": {Provider: "groq", Model: "llama"},
		}
		for attemptName, attempt := range attempts {
			b.Run(shape.name+"/"+attemptName, func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					_ = svc.ResolveRequestOverrides(ctx, resolution, attempt)
				}
			})
		}
	}
}

// BenchmarkResolveFailovers measures building the failover chain after a
// request resolved through a redirect.
func BenchmarkResolveFailovers(b *testing.B) {
	for _, source := range []string{"flat", "chained"} {
		b.Run(source, func(b *testing.B) {
			svc := benchService(b)
			requested := core.NewRequestedModelSelector(source, "")
			resolved, applied, err := svc.ResolveModel(requested)
			if err != nil || !applied {
				b.Fatalf("ResolveModel() = %v, %v, %v", resolved, applied, err)
			}
			resolution := &core.RequestModelResolution{Requested: requested, ResolvedSelector: resolved, AliasApplied: applied}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if chain := svc.ResolveFailovers(resolution, core.OperationChatCompletions); len(chain) == 0 {
					b.Fatal("empty chain")
				}
			}
		})
	}
}

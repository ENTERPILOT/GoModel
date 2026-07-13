package usage

import (
	"context"
	"database/sql"
	"math"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestApplyRewriteSavings(t *testing.T) {
	flatPricing := &core.ModelPricing{InputPerMtok: new(2.0), OutputPerMtok: new(8.0)}

	t.Run("nil entry and non-positive savings are no-ops", func(t *testing.T) {
		ApplyRewriteSavings(nil, 100, flatPricing)

		entry := &UsageEntry{InputTokens: 500}
		ApplyRewriteSavings(entry, 0, flatPricing)
		if entry.RewriteTokensSaved != 0 || entry.RewriteCostSaved != nil {
			t.Fatalf("zero savings must not touch the entry, got tokens=%d cost=%v", entry.RewriteTokensSaved, entry.RewriteCostSaved)
		}
	})

	t.Run("nil pricing records tokens without cost", func(t *testing.T) {
		entry := &UsageEntry{InputTokens: 500}
		ApplyRewriteSavings(entry, 250, nil)
		if entry.RewriteTokensSaved != 250 {
			t.Fatalf("tokens saved = %d, want 250", entry.RewriteTokensSaved)
		}
		if entry.RewriteCostSaved != nil {
			t.Fatalf("cost saved must stay nil without pricing, got %v", *entry.RewriteCostSaved)
		}
	})

	t.Run("flat input rate prices the removed tokens", func(t *testing.T) {
		entry := &UsageEntry{Endpoint: "/v1/chat/completions", Provider: "openai", InputTokens: 1000, OutputTokens: 100}
		ApplyRewriteSavings(entry, 500_000, flatPricing)
		if entry.RewriteTokensSaved != 500_000 {
			t.Fatalf("tokens saved = %d, want 500000", entry.RewriteTokensSaved)
		}
		if entry.RewriteCostSaved == nil {
			t.Fatal("expected a cost estimate with flat pricing")
		}
		// 500k tokens at $2/Mtok = $1.00; output rate must not leak in.
		if got, want := *entry.RewriteCostSaved, 1.0; math.Abs(got-want) > 1e-9 {
			t.Fatalf("cost saved = %v, want %v", got, want)
		}
	})

	t.Run("tier crossing re-rates the whole input", func(t *testing.T) {
		tiered := &core.ModelPricing{
			InputPerMtok: new(10.0),
			Tiers: []core.ModelPricingTier{
				{UpToTokens: new(float64(1000)), InputPerMtok: new(1.0)},
				{UpToTokens: new(float64(1_000_000)), InputPerMtok: new(3.0)},
			},
		}
		entry := &UsageEntry{Endpoint: "/v1/chat/completions", Provider: "openai", InputTokens: 800}
		ApplyRewriteSavings(entry, 400, tiered)
		if entry.RewriteCostSaved == nil {
			t.Fatal("expected a cost estimate with tiered pricing")
		}
		// As forwarded: 800 tokens in the $1/Mtok tier = $0.0008.
		// As sent: 1200 tokens land in the $3/Mtok tier = $0.0036.
		if got, want := *entry.RewriteCostSaved, 0.0036-0.0008; math.Abs(got-want) > 1e-9 {
			t.Fatalf("cost saved = %v, want %v", got, want)
		}
	})

	t.Run("batch endpoint uses batch input rate", func(t *testing.T) {
		pricing := &core.ModelPricing{InputPerMtok: new(2.0), BatchInputPerMtok: new(1.0)}
		entry := &UsageEntry{Endpoint: "/v1/batches", Provider: "openai", InputTokens: 0}
		ApplyRewriteSavings(entry, 1_000_000, pricing)
		if entry.RewriteCostSaved == nil {
			t.Fatal("expected a cost estimate for the batch endpoint")
		}
		if got, want := *entry.RewriteCostSaved, 1.0; math.Abs(got-want) > 1e-9 {
			t.Fatalf("cost saved = %v, want %v (batch rate)", got, want)
		}
	})
}

func TestSQLiteSummaryAggregatesRewriteSavings(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open sqlite database: %v", err)
	}
	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}

	ctx := context.Background()
	entries := []*UsageEntry{
		{
			ID: "with-savings", RequestID: "req-1", ProviderID: "p-1",
			Timestamp: time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC),
			Model:     "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions",
			InputTokens: 1000, OutputTokens: 50, TotalTokens: 1050,
			RewriteTokensSaved: 400, RewriteCostSaved: new(0.0008),
		},
		{
			ID: "with-unpriced-savings", RequestID: "req-2", ProviderID: "p-2",
			Timestamp: time.Date(2026, 7, 1, 11, 0, 0, 0, time.UTC),
			Model:     "local-model", Provider: "ollama", Endpoint: "/v1/chat/completions",
			InputTokens: 500, OutputTokens: 20, TotalTokens: 520,
			RewriteTokensSaved: 100,
		},
		{
			ID: "no-savings", RequestID: "req-3", ProviderID: "p-3",
			Timestamp: time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
			Model:     "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions",
			InputTokens: 200, OutputTokens: 10, TotalTokens: 210,
		},
	}
	if err := store.WriteBatch(ctx, entries); err != nil {
		t.Fatalf("failed to seed usage entries: %v", err)
	}

	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("failed to create sqlite reader: %v", err)
	}

	summary, err := reader.GetSummary(ctx, UsageQueryParams{})
	if err != nil {
		t.Fatalf("GetSummary returned error: %v", err)
	}
	if summary.RewriteTokensSaved != 500 {
		t.Fatalf("rewrite tokens saved = %d, want 500", summary.RewriteTokensSaved)
	}
	if summary.RewriteCostSaved == nil {
		t.Fatal("expected an aggregated rewrite cost")
	}
	if got, want := *summary.RewriteCostSaved, 0.0008; math.Abs(got-want) > 1e-9 {
		t.Fatalf("rewrite cost saved = %v, want %v", got, want)
	}

	// A slice with no priced savings keeps the cost null rather than zero.
	unpriced, err := reader.GetSummary(ctx, UsageQueryParams{Provider: "ollama"})
	if err != nil {
		t.Fatalf("GetSummary(ollama) returned error: %v", err)
	}
	if unpriced.RewriteTokensSaved != 100 {
		t.Fatalf("ollama rewrite tokens saved = %d, want 100", unpriced.RewriteTokensSaved)
	}
	if unpriced.RewriteCostSaved != nil {
		t.Fatalf("ollama rewrite cost saved = %v, want nil", *unpriced.RewriteCostSaved)
	}
}

type staticSavingsPricingResolver struct {
	pricing *core.ModelPricing
}

func (r staticSavingsPricingResolver) ResolvePricing(_, _ string) *core.ModelPricing {
	return r.pricing
}

func TestSQLiteRecalculatePricingRefreshesRewriteCostSaved(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open sqlite database: %v", err)
	}
	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}

	ctx := context.Background()
	if err := store.WriteBatch(ctx, []*UsageEntry{{
		ID: "stale-savings", RequestID: "req-1", ProviderID: "p-1",
		Timestamp: time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC),
		Model:     "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions",
		InputTokens: 1000, OutputTokens: 50, TotalTokens: 1050,
		RewriteTokensSaved: 500_000, RewriteCostSaved: new(123.0),
	}}); err != nil {
		t.Fatalf("failed to seed usage entry: %v", err)
	}

	resolver := staticSavingsPricingResolver{pricing: &core.ModelPricing{
		InputPerMtok:  new(2.0),
		OutputPerMtok: new(8.0),
	}}
	if _, err := store.RecalculatePricing(ctx, RecalculatePricingParams{}, resolver); err != nil {
		t.Fatalf("RecalculatePricing returned error: %v", err)
	}

	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("failed to create sqlite reader: %v", err)
	}
	summary, err := reader.GetSummary(ctx, UsageQueryParams{})
	if err != nil {
		t.Fatalf("GetSummary returned error: %v", err)
	}
	if summary.RewriteTokensSaved != 500_000 {
		t.Fatalf("rewrite tokens saved = %d, want 500000", summary.RewriteTokensSaved)
	}
	if summary.RewriteCostSaved == nil {
		t.Fatal("expected recalculated rewrite cost")
	}
	if got, want := *summary.RewriteCostSaved, 1.0; math.Abs(got-want) > 1e-9 {
		t.Fatalf("recalculated rewrite cost = %v, want %v (500k tokens at $2/Mtok)", got, want)
	}
}

package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

func TestApplyRewriteSavings(t *testing.T) {
	flatPricing := &core.ModelPricing{InputPerMtok: new(2.0), OutputPerMtok: new(8.0)}

	t.Run("nil entry and non-positive savings are no-ops", func(t *testing.T) {
		ApplyRewriteSavings(nil, 100, flatPricing)

		entry := &UsageEntry{InputTokens: 500}
		ApplyRewriteSavings(entry, 0, flatPricing)
		require.Equal(t, 0, entry.RewriteTokensSaved)
		require.Nil(t, entry.RewriteCostSaved)
	})

	t.Run("nil pricing records tokens without cost", func(t *testing.T) {
		entry := &UsageEntry{InputTokens: 500}
		ApplyRewriteSavings(entry, 250, nil)
		require.Equal(t, 250, entry.RewriteTokensSaved)
		require.Nil(t, entry.RewriteCostSaved)
	})

	t.Run("flat input rate prices the removed tokens", func(t *testing.T) {
		entry := &UsageEntry{Endpoint: "/v1/chat/completions", Provider: "openai", InputTokens: 1000, OutputTokens: 100}
		ApplyRewriteSavings(entry, 500_000, flatPricing)
		require.Equal(t, 500_000, entry.RewriteTokensSaved)
		require.NotNil(t, entry.RewriteCostSaved)

		// 500k tokens at $2/Mtok = $1.00; output rate must not leak in.
		require.InDelta(t, 1.0, *entry.RewriteCostSaved, 1e-9)
	})

	t.Run("observed blended rate includes OpenAI prompt-cache reads", func(t *testing.T) {
		// 20k uncached at $3/Mtok + 80k cached at $0.30/Mtok = $0.084.
		// The 10k removed tokens inherit that observed $0.84/Mtok blend.
		inputCost := 0.084
		entry := &UsageEntry{
			Provider:    "openai",
			InputTokens: 100_000,
			InputCost:   &inputCost,
			RawData:     map[string]any{"prompt_cached_tokens": 80_000},
		}
		ApplyRewriteSavings(entry, 10_000, nil)
		require.NotNil(t, entry.RewriteCostSaved)

		require.InDelta(t, 0.0084, *entry.RewriteCostSaved, 1e-9, "observed blended input rate")
	})

	t.Run("observed blended rate includes Anthropic additive cache parts", func(t *testing.T) {
		// Anthropic input_tokens is the 20k uncached portion; cache reads and
		// writes are additive, making 110k full input parts behind $0.1215.
		inputCost := 0.1215
		entry := &UsageEntry{
			Provider:    "anthropic",
			InputTokens: 20_000,
			InputCost:   &inputCost,
			RawData: map[string]any{
				"cache_read_input_tokens":     80_000,
				"cache_creation_input_tokens": 10_000,
			},
		}
		ApplyRewriteSavings(entry, 10_000, flatPricing)
		require.NotNil(t, entry.RewriteCostSaved)

		require.InDelta(t, inputCost*10_000/110_000, *entry.RewriteCostSaved, 1e-9, "all additive input parts")
	})

	t.Run("observed rate excludes retained fixed input charges", func(t *testing.T) {
		tests := []struct {
			name      string
			provider  string
			inputCost float64
			pricing   *core.ModelPricing
			rawData   map[string]any
			want      float64
		}{
			{
				name:      "xAI image",
				provider:  "xai",
				inputCost: 0.0092,
				pricing: &core.ModelPricing{
					InputPerMtok:  new(2.0),
					InputPerImage: new(0.009),
				},
				rawData: map[string]any{"image_tokens": 1},
				want:    0.0018,
			},
			{
				name:      "audio second",
				provider:  "openai",
				inputCost: 0.0502,
				pricing: &core.ModelPricing{
					InputPerMtok:   new(2.0),
					PerSecondInput: new(0.05),
				},
				rawData: map[string]any{rawKeyAudioSeconds: 1.0},
				want:    0.0018,
			},
			{
				name:      "input characters",
				provider:  "openai",
				inputCost: 0.0502,
				pricing: &core.ModelPricing{
					InputPerMtok:      new(2.0),
					PerCharacterInput: new(0.00005),
				},
				rawData: map[string]any{rawKeyInputCharacters: 1000},
				want:    0.0018,
			},
		}

		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				inputCost := test.inputCost
				entry := &UsageEntry{
					Provider:    test.provider,
					InputTokens: 100,
					InputCost:   &inputCost,
					RawData:     test.rawData,
				}
				ApplyRewriteSavings(entry, 900, test.pricing)
				require.NotNil(t, entry.RewriteCostSaved)

				require.InDelta(t, test.want, *entry.RewriteCostSaved, 1e-9, "retained fixed charge excluded")
			})
		}
	})

	t.Run("unseparated fixed charges do not use the observed shortcut", func(t *testing.T) {
		inputCost := 0.0092
		entry := &UsageEntry{
			Provider:    "xai",
			InputTokens: 100,
			InputCost:   &inputCost,
			RawData:     map[string]any{"image_tokens": 1},
		}
		ApplyRewriteSavings(entry, 900, nil)
		require.Nil(t, entry.RewriteCostSaved)
	})

	t.Run("tier crossing re-rates the whole input with a request cost present", func(t *testing.T) {
		tiered := &core.ModelPricing{
			InputPerMtok: new(10.0),
			Tiers: []core.ModelPricingTier{
				{UpToTokens: new(float64(1000)), InputPerMtok: new(1.0)},
				{UpToTokens: new(float64(1_000_000)), InputPerMtok: new(3.0)},
			},
		}
		inputCost := 0.0008
		entry := &UsageEntry{
			Endpoint:    "/v1/chat/completions",
			Provider:    "openai",
			InputTokens: 800,
			InputCost:   &inputCost,
		}
		ApplyRewriteSavings(entry, 400, tiered)
		require.NotNil(t, entry.RewriteCostSaved)

		// As forwarded: 800 tokens in the $1/Mtok tier = $0.0008.
		// As sent: 1200 tokens land in the $3/Mtok tier = $0.0036.
		require.InDelta(t, 0.0036-0.0008, *entry.RewriteCostSaved, 1e-9)
	})

	t.Run("batch endpoint uses batch input rate", func(t *testing.T) {
		pricing := &core.ModelPricing{InputPerMtok: new(2.0), BatchInputPerMtok: new(1.0)}
		entry := &UsageEntry{Endpoint: "/v1/batches", Provider: "openai", InputTokens: 0}
		ApplyRewriteSavings(entry, 1_000_000, pricing)
		require.NotNil(t, entry.RewriteCostSaved)

		require.InDelta(t, 1.0, *entry.RewriteCostSaved, 1e-9, "batch rate")
	})
}

func TestRecalculatePricingUsesObservedBlendedRateForRewriteSavings(t *testing.T) {
	update := recalculateEntryCosts(recalculationEntry{
		ID:                 "cached-savings",
		Model:              "gpt-5",
		Provider:           "openai",
		Endpoint:           "/v1/chat/completions",
		InputTokens:        100_000,
		RewriteTokensSaved: 10_000,
		RawData:            map[string]any{"prompt_cached_tokens": 80_000},
	}, staticSavingsPricingResolver{pricing: &core.ModelPricing{
		InputPerMtok:       new(3.0),
		CachedInputPerMtok: new(0.30),
	}})

	require.NotNil(t, update.RewriteCostSaved)

	// Recalculated input cost is $0.084 over 100k full input parts, so
	// 10k removed tokens inherit $0.0084 of the observed blended cost.
	require.InDelta(t, 0.0084, *update.RewriteCostSaved, 1e-9)
}

func TestSQLiteSummaryAggregatesRewriteSavings(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	require.NoError(t, err)

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
	err = store.WriteBatch(ctx, entries)
	require.NoError(t, err)

	reader, err := NewSQLiteReader(db)
	require.NoError(t, err)

	summary, err := reader.GetSummary(ctx, UsageQueryParams{})
	require.NoError(t, err)
	require.Equal(t, int64(500), summary.RewriteTokensSaved)
	require.NotNil(t, summary.RewriteCostSaved)

	require.InDelta(t, 0.0008, *summary.RewriteCostSaved, 1e-9)

	// A slice with no priced savings keeps the cost null rather than zero.
	unpriced, err := reader.GetSummary(ctx, UsageQueryParams{Provider: "ollama"})
	require.NoError(t, err)
	require.Equal(t, int64(100), unpriced.RewriteTokensSaved)
	require.Nil(t, unpriced.RewriteCostSaved)
}

type staticSavingsPricingResolver struct {
	pricing *core.ModelPricing
}

func (r staticSavingsPricingResolver) ResolvePricing(_, _ string) *core.ModelPricing {
	return r.pricing
}

func TestSQLiteRecalculatePricingRefreshesRewriteCostSaved(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	require.NoError(t, err)

	ctx := context.Background()
	err = store.WriteBatch(ctx, []*UsageEntry{{
		ID: "stale-savings", RequestID: "req-1", ProviderID: "p-1",
		Timestamp: time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC),
		Model:     "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions",
		InputTokens: 1000, OutputTokens: 50, TotalTokens: 1050,
		RewriteTokensSaved: 500_000, RewriteCostSaved: new(123.0),
	}})
	require.NoError(t, err)

	resolver := staticSavingsPricingResolver{pricing: &core.ModelPricing{
		InputPerMtok:  new(2.0),
		OutputPerMtok: new(8.0),
	}}
	_, err = store.RecalculatePricing(ctx, RecalculatePricingParams{}, resolver)
	require.NoError(t, err)

	reader, err := NewSQLiteReader(db)
	require.NoError(t, err)

	summary, err := reader.GetSummary(ctx, UsageQueryParams{})
	require.NoError(t, err)
	require.Equal(t, int64(500_000), summary.RewriteTokensSaved)
	require.NotNil(t, summary.RewriteCostSaved)

	require.InDelta(t, 1.0, *summary.RewriteCostSaved, 1e-9, "500k tokens at $2/Mtok")
}

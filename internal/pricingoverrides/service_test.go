package pricingoverrides

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

type testStore struct {
	items     map[string]Override
	listErrs  []error
	upsertErr error
	deleteErr error
}

func newTestStore(items ...Override) *testStore {
	store := &testStore{items: make(map[string]Override, len(items))}
	for _, item := range items {
		store.items[item.Selector] = item
	}
	return store
}

func (s *testStore) List(_ context.Context) ([]Override, error) {
	if len(s.listErrs) > 0 {
		err := s.listErrs[0]
		s.listErrs = s.listErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	result := make([]Override, 0, len(s.items))
	for _, item := range s.items {
		result = append(result, item)
	}
	return result, nil
}

func (s *testStore) Upsert(_ context.Context, override Override) error {
	if s.upsertErr != nil {
		return s.upsertErr
	}
	s.items[override.Selector] = override
	return nil
}

func (s *testStore) Delete(_ context.Context, selector string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	if _, ok := s.items[selector]; !ok {
		return ErrNotFound
	}
	delete(s.items, selector)
	return nil
}

func (s *testStore) Close() error { return nil }

type testCatalog struct {
	providerNames []string
}

func (c testCatalog) ProviderNames() []string {
	return append([]string(nil), c.providerNames...)
}

type staticPricingResolver struct {
	pricing *core.ModelPricing
}

func (r staticPricingResolver) ResolvePricing(_, _ string) *core.ModelPricing {
	return r.pricing
}

type selectivePricingResolver map[string]*core.ModelPricing

func (r selectivePricingResolver) ResolvePricing(model, provider string) *core.ModelPricing {
	return r[provider+"/"+model]
}

func TestServiceResolvePricingAppliesMostSpecificOverride(t *testing.T) {
	baseInput := 1.0
	baseOutput := 2.0
	service, err := NewService(
		newTestStore(
			Override{Selector: "/", Pricing: Pricing{InputPerMtok: new(float64(10))}},
			Override{Selector: "openai/", Pricing: Pricing{InputPerMtok: new(float64(20))}},
			Override{Selector: "gpt-4o", Pricing: Pricing{InputPerMtok: new(float64(30))}},
			Override{Selector: "openai/gpt-4o", Pricing: Pricing{InputPerMtok: new(float64(40))}},
		),
		testCatalog{providerNames: []string{"openai"}},
		staticPricingResolver{pricing: &core.ModelPricing{
			InputPerMtok:  &baseInput,
			OutputPerMtok: &baseOutput,
		}},
	)
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	pricing := service.ResolvePricing("gpt-4o", "openai")
	require.NotNil(t, pricing)
	require.NotNil(t, pricing.InputPerMtok)
	require.Equal(t, float64(40), *pricing.InputPerMtok)
	require.NotNil(t, pricing.OutputPerMtok)
	require.Equal(t, baseOutput, *pricing.OutputPerMtok)
	require.Equal(t, CurrencyUSD, pricing.Currency)
}

func TestServiceResolvePricingModelWideBeatsProviderWide(t *testing.T) {
	service, err := NewService(
		newTestStore(
			Override{Selector: "openai/", Pricing: Pricing{InputPerMtok: new(float64(20))}},
			Override{Selector: "gpt-4o", Pricing: Pricing{InputPerMtok: new(float64(30))}},
		),
		testCatalog{providerNames: []string{"openai"}},
		nil,
	)
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	pricing := service.ResolvePricing("gpt-4o", "openai")
	require.NotNil(t, pricing)
	require.NotNil(t, pricing.InputPerMtok)
	require.Equal(t, float64(30), *pricing.InputPerMtok)
}

func TestServiceResolvePricingPreservesSlashShapedModelIDs(t *testing.T) {
	service, err := NewService(
		newTestStore(
			Override{Selector: "openrouter/", Pricing: Pricing{InputPerMtok: new(float64(20))}},
			Override{Selector: "anthropic/claude-sonnet", Pricing: Pricing{InputPerMtok: new(float64(30))}},
			Override{Selector: "openrouter/anthropic/claude-sonnet", Pricing: Pricing{InputPerMtok: new(float64(40))}},
		),
		testCatalog{providerNames: []string{"openrouter"}},
		nil,
	)
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	pricing := service.ResolvePricing("anthropic/claude-sonnet", "openrouter")
	require.NotNil(t, pricing)
	require.NotNil(t, pricing.InputPerMtok)
	require.Equal(t, float64(40), *pricing.InputPerMtok)

	pricing = service.ResolvePricing("openrouter/anthropic/claude-sonnet", "openrouter")
	require.NotNil(t, pricing)
	require.NotNil(t, pricing.InputPerMtok)
	require.Equal(t, float64(40), *pricing.InputPerMtok)
}

func TestServiceResolvePricingFallsBackToRawProviderOwnedModelForBasePricing(t *testing.T) {
	baseInput := 1.0
	service, err := NewService(
		newTestStore(),
		testCatalog{providerNames: []string{"openrouter"}},
		selectivePricingResolver{
			"openrouter/openrouter/free": {InputPerMtok: &baseInput},
		},
	)
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	pricing := service.ResolvePricing("openrouter/free", "openrouter")
	require.NotNil(t, pricing)
	require.NotNil(t, pricing.InputPerMtok)
	require.Equal(t, baseInput, *pricing.InputPerMtok)
}

func TestServiceResolvePricingSlashShapedModelWideBeatsProviderWide(t *testing.T) {
	service, err := NewService(
		newTestStore(
			Override{Selector: "openrouter/", Pricing: Pricing{InputPerMtok: new(float64(20))}},
			Override{Selector: "anthropic/claude-sonnet", Model: "anthropic/claude-sonnet", Pricing: Pricing{InputPerMtok: new(float64(30))}},
		),
		testCatalog{providerNames: []string{"openrouter"}},
		nil,
	)
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	pricing := service.ResolvePricing("anthropic/claude-sonnet", "openrouter")
	require.NotNil(t, pricing)
	require.NotNil(t, pricing.InputPerMtok)
	require.Equal(t, float64(30), *pricing.InputPerMtok)
}

func TestServiceRejectsEmptyAndNegativePricing(t *testing.T) {
	service, err := NewService(newTestStore(), testCatalog{providerNames: []string{"openai"}}, nil)
	require.NoError(t, err)
	err = service.Upsert(context.Background(), Override{Selector: "openai/gpt-4o"})
	require.True(t, IsValidationError(err))
	err = service.Upsert(context.Background(), Override{
		Selector: "openai/gpt-4o",
		Pricing:  Pricing{InputPerMtok: new(float64(-1))},
	})
	require.True(t, IsValidationError(err))
}

func TestServiceRejectsInvalidTieredPricing(t *testing.T) {
	service, err := NewService(newTestStore(), testCatalog{providerNames: []string{"openai"}}, nil)
	require.NoError(t, err)

	cases := []struct {
		name    string
		pricing Pricing
	}{
		{
			name: "missing threshold",
			pricing: Pricing{Tiers: []PricingTier{
				{InputPerMtok: new(float64(1))},
			}},
		},
		{
			name: "missing rate",
			pricing: Pricing{Tiers: []PricingTier{
				{UpToTokens: new(float64(1000))},
			}},
		},
		{
			name: "non-increasing thresholds",
			pricing: Pricing{Tiers: []PricingTier{
				{UpToTokens: new(float64(1000)), InputPerMtok: new(float64(1))},
				{UpToTokens: new(float64(500)), InputPerMtok: new(float64(2))},
			}},
		},
		{
			name: "zero threshold",
			pricing: Pricing{Tiers: []PricingTier{
				{UpToMtok: new(float64(0)), InputPerMtok: new(float64(1))},
			}},
		},
		{
			name: "both threshold units",
			pricing: Pricing{Tiers: []PricingTier{
				{UpToTokens: new(float64(1000)), UpToMtok: new(float64(1)), InputPerMtok: new(float64(1))},
			}},
		},
		{
			name: "negative tier rate",
			pricing: Pricing{Tiers: []PricingTier{
				{UpToTokens: new(float64(1000)), InputPerMtok: new(float64(-1))},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := service.Upsert(context.Background(), Override{
				Selector: "openai/gpt-4o",
				Pricing:  tc.pricing,
			})
			require.True(t, IsValidationError(err), "Upsert(%s) error = %v, want validation", tc.name, err)
		})
	}
}

func TestServiceAcceptsIncreasingTieredPricing(t *testing.T) {
	service, err := NewService(newTestStore(), testCatalog{providerNames: []string{"openai"}}, nil)
	require.NoError(t, err)

	err = service.Upsert(context.Background(), Override{
		Selector: "openai/gpt-4o",
		Pricing: Pricing{Tiers: []PricingTier{
			{UpToTokens: new(float64(200_000)), InputPerMtok: new(float64(1))},
			{UpToMtok: new(float64(1)), InputPerMtok: new(0.5)},
		}},
	})
	require.NoError(t, err)
}

func TestServiceReconcilesSnapshotWhenUpsertRollbackFails(t *testing.T) {
	store := newTestStore()
	service, err := NewService(store, testCatalog{providerNames: []string{"openai"}}, nil)
	require.NoError(t, err)

	store.listErrs = []error{errors.New("list failed"), nil}
	store.deleteErr = errors.New("rollback delete failed")
	err = service.Upsert(context.Background(), Override{
		Selector: "openai/gpt-4o",
		Pricing:  Pricing{InputPerMtok: new(float64(9))},
	})
	require.Error(t, err)

	pricing := service.ResolvePricing("gpt-4o", "openai")
	require.NotNil(t, pricing)
	require.NotNil(t, pricing.InputPerMtok)
	require.Equal(t, float64(9), *pricing.InputPerMtok)
}

func TestServiceReconcilesSnapshotWhenDeleteRollbackFails(t *testing.T) {
	store := newTestStore(Override{
		Selector:     "openai/gpt-4o",
		ProviderName: "openai",
		Model:        "gpt-4o",
		Pricing:      Pricing{InputPerMtok: new(float64(9))},
	})
	service, err := NewService(store, testCatalog{providerNames: []string{"openai"}}, nil)
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	store.listErrs = []error{errors.New("list failed"), nil}
	store.upsertErr = errors.New("rollback upsert failed")
	err = service.Delete(context.Background(), "openai/gpt-4o")
	require.Error(t, err)
	_, ok := service.Get("openai/gpt-4o")
	require.False(t, ok)
}

func TestServiceBuildSnapshotRejectsDuplicateNormalizedSelectors(t *testing.T) {
	service, err := NewService(newTestStore(), testCatalog{providerNames: []string{"openai"}}, nil)
	require.NoError(t, err)

	_, err = service.buildSnapshot([]Override{
		{Selector: "openai/gpt-4o", ProviderName: "openai", Model: "gpt-4o", Pricing: Pricing{InputPerMtok: new(float64(1))}},
		{Selector: " openai/gpt-4o ", ProviderName: "openai", Model: "gpt-4o", Pricing: Pricing{InputPerMtok: new(float64(2))}},
	})
	require.Error(t, err)

	var duplicateErr *DuplicateSelectorError
	require.ErrorAs(t, err, &duplicateErr)
	require.Equal(t, "openai/gpt-4o", duplicateErr.Normalized)
	require.Equal(t, " openai/gpt-4o ", duplicateErr.Original)
	require.Equal(t, "openai/gpt-4o", duplicateErr.Existing)
}

func TestNormalizedRefreshIntervalClampsBelowRefreshTimeout(t *testing.T) {
	tests := []struct {
		name     string
		interval time.Duration
		want     time.Duration
	}{
		{name: "default", interval: 0, want: time.Minute},
		{name: "negative default", interval: -time.Second, want: time.Minute},
		{name: "below timeout", interval: time.Second, want: refreshTimeout},
		{name: "at timeout", interval: refreshTimeout, want: refreshTimeout},
		{name: "above timeout", interval: time.Minute, want: time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizedRefreshInterval(tt.interval)
			require.Equal(t, tt.want, got, "normalizedRefreshInterval(%s) = %s, want %s", tt.interval, got, tt.want)
		})
	}
}

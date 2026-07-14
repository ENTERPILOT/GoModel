package pricingoverrides

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
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
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	pricing := service.ResolvePricing("gpt-4o", "openai")
	if pricing == nil {
		t.Fatal("ResolvePricing() = nil")
	}
	if pricing.InputPerMtok == nil || *pricing.InputPerMtok != 40 {
		t.Fatalf("InputPerMtok = %#v, want 40", pricing.InputPerMtok)
	}
	if pricing.OutputPerMtok == nil || *pricing.OutputPerMtok != baseOutput {
		t.Fatalf("OutputPerMtok = %#v, want base %v", pricing.OutputPerMtok, baseOutput)
	}
	if pricing.Currency != CurrencyUSD {
		t.Fatalf("Currency = %q, want %q", pricing.Currency, CurrencyUSD)
	}
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
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	pricing := service.ResolvePricing("gpt-4o", "openai")
	if pricing == nil || pricing.InputPerMtok == nil || *pricing.InputPerMtok != 30 {
		t.Fatalf("ResolvePricing() = %+v, want model-wide input rate 30", pricing)
	}
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
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	pricing := service.ResolvePricing("anthropic/claude-sonnet", "openrouter")
	if pricing == nil || pricing.InputPerMtok == nil || *pricing.InputPerMtok != 40 {
		t.Fatalf("ResolvePricing(slash-shaped exact) = %+v, want exact input rate 40", pricing)
	}

	pricing = service.ResolvePricing("openrouter/anthropic/claude-sonnet", "openrouter")
	if pricing == nil || pricing.InputPerMtok == nil || *pricing.InputPerMtok != 40 {
		t.Fatalf("ResolvePricing(redundant provider prefix) = %+v, want exact input rate 40", pricing)
	}
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
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	pricing := service.ResolvePricing("openrouter/free", "openrouter")
	if pricing == nil || pricing.InputPerMtok == nil || *pricing.InputPerMtok != baseInput {
		t.Fatalf("ResolvePricing(raw provider-owned base) = %+v, want base input rate %v", pricing, baseInput)
	}
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
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	pricing := service.ResolvePricing("anthropic/claude-sonnet", "openrouter")
	if pricing == nil || pricing.InputPerMtok == nil || *pricing.InputPerMtok != 30 {
		t.Fatalf("ResolvePricing(slash-shaped model-wide) = %+v, want model-wide input rate 30", pricing)
	}
}

func TestServiceRejectsEmptyAndNegativePricing(t *testing.T) {
	service, err := NewService(newTestStore(), testCatalog{providerNames: []string{"openai"}}, nil)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	if err := service.Upsert(context.Background(), Override{Selector: "openai/gpt-4o"}); !IsValidationError(err) {
		t.Fatalf("Upsert(empty) error = %v, want validation", err)
	}
	if err := service.Upsert(context.Background(), Override{
		Selector: "openai/gpt-4o",
		Pricing:  Pricing{InputPerMtok: new(float64(-1))},
	}); !IsValidationError(err) {
		t.Fatalf("Upsert(negative) error = %v, want validation", err)
	}
}

func TestServiceRejectsInvalidTieredPricing(t *testing.T) {
	service, err := NewService(newTestStore(), testCatalog{providerNames: []string{"openai"}}, nil)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

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
			if !IsValidationError(err) {
				t.Fatalf("Upsert(%s) error = %v, want validation", tc.name, err)
			}
		})
	}
}

func TestServiceAcceptsIncreasingTieredPricing(t *testing.T) {
	service, err := NewService(newTestStore(), testCatalog{providerNames: []string{"openai"}}, nil)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	err = service.Upsert(context.Background(), Override{
		Selector: "openai/gpt-4o",
		Pricing: Pricing{Tiers: []PricingTier{
			{UpToTokens: new(float64(200_000)), InputPerMtok: new(float64(1))},
			{UpToMtok: new(float64(1)), InputPerMtok: new(0.5)},
		}},
	})
	if err != nil {
		t.Fatalf("Upsert(valid tiers) error = %v", err)
	}
}

func TestServiceReconcilesSnapshotWhenUpsertRollbackFails(t *testing.T) {
	store := newTestStore()
	service, err := NewService(store, testCatalog{providerNames: []string{"openai"}}, nil)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	store.listErrs = []error{errors.New("list failed"), nil}
	store.deleteErr = errors.New("rollback delete failed")
	err = service.Upsert(context.Background(), Override{
		Selector: "openai/gpt-4o",
		Pricing:  Pricing{InputPerMtok: new(float64(9))},
	})
	if err == nil {
		t.Fatal("Upsert() error = nil, want refresh/rollback error")
	}

	pricing := service.ResolvePricing("gpt-4o", "openai")
	if pricing == nil || pricing.InputPerMtok == nil || *pricing.InputPerMtok != 9 {
		t.Fatalf("ResolvePricing() = %+v, want reconciled persisted override", pricing)
	}
}

func TestServiceReconcilesSnapshotWhenDeleteRollbackFails(t *testing.T) {
	store := newTestStore(Override{
		Selector:     "openai/gpt-4o",
		ProviderName: "openai",
		Model:        "gpt-4o",
		Pricing:      Pricing{InputPerMtok: new(float64(9))},
	})
	service, err := NewService(store, testCatalog{providerNames: []string{"openai"}}, nil)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	store.listErrs = []error{errors.New("list failed"), nil}
	store.upsertErr = errors.New("rollback upsert failed")
	err = service.Delete(context.Background(), "openai/gpt-4o")
	if err == nil {
		t.Fatal("Delete() error = nil, want refresh/rollback error")
	}

	if _, ok := service.Get("openai/gpt-4o"); ok {
		t.Fatal("Get() found deleted override after rollback failure, want reconciled persisted state")
	}
}

func TestServiceBuildSnapshotRejectsDuplicateNormalizedSelectors(t *testing.T) {
	service, err := NewService(newTestStore(), testCatalog{providerNames: []string{"openai"}}, nil)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	_, err = service.buildSnapshot([]Override{
		{Selector: "openai/gpt-4o", ProviderName: "openai", Model: "gpt-4o", Pricing: Pricing{InputPerMtok: new(float64(1))}},
		{Selector: " openai/gpt-4o ", ProviderName: "openai", Model: "gpt-4o", Pricing: Pricing{InputPerMtok: new(float64(2))}},
	})
	if err == nil {
		t.Fatal("buildSnapshot() error = nil, want duplicate selector error")
	}
	var duplicateErr *DuplicateSelectorError
	if !errors.As(err, &duplicateErr) {
		t.Fatalf("buildSnapshot() error = %T %v, want *DuplicateSelectorError", err, err)
	}
	if duplicateErr.Normalized != "openai/gpt-4o" ||
		duplicateErr.Original != " openai/gpt-4o " ||
		duplicateErr.Existing != "openai/gpt-4o" {
		t.Fatalf("DuplicateSelectorError = %+v, want normalized/original/existing selector details", duplicateErr)
	}
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
			if got := normalizedRefreshInterval(tt.interval); got != tt.want {
				t.Fatalf("normalizedRefreshInterval(%s) = %s, want %s", tt.interval, got, tt.want)
			}
		})
	}
}

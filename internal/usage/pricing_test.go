package usage

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/enterpilot/gomodel/internal/core"
)

// checkedPricingResolver prices models from exact entries, falling back to a
// broad (provider-wide) rule, and reports which prices are exact.
type checkedPricingResolver struct {
	exact map[string]*core.ModelPricing
	broad *core.ModelPricing
}

func (r checkedPricingResolver) ResolvePricing(model, _ string) *core.ModelPricing {
	if pricing, ok := r.exact[model]; ok {
		return pricing
	}
	return r.broad
}

func (r checkedPricingResolver) HasModelPricing(model, _ string) bool {
	_, ok := r.exact[model]
	return ok
}

func TestResolveServedModelPricing(t *testing.T) {
	rate := func(v float64) *core.ModelPricing { return &core.ModelPricing{InputPerMtok: &v} }
	routedExact, answeredExact, broad := rate(1), rate(2), rate(3)

	tests := []struct {
		name     string
		resolver PricingResolver
		want     *core.ModelPricing
	}{
		{
			name:     "exact routed price wins",
			resolver: checkedPricingResolver{exact: map[string]*core.ModelPricing{"jev-latest": routedExact, "jev-1.13.0": answeredExact}, broad: broad},
			want:     routedExact,
		},
		{
			name:     "exact answering price beats a broad routed rule",
			resolver: checkedPricingResolver{exact: map[string]*core.ModelPricing{"jev-1.13.0": answeredExact}, broad: broad},
			want:     answeredExact,
		},
		{
			name:     "broad rule when neither is exact",
			resolver: checkedPricingResolver{broad: broad},
			want:     broad,
		},
		{
			name:     "resolver without exact checks tries routed then answering",
			resolver: mapPricingResolver{"jev-1.13.0/jev": answeredExact},
			want:     answeredExact,
		},
		{
			name: "no resolver",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveServedModelPricing(tt.resolver, "jev-latest", "jev", func() string { return "jev-1.13.0" })
			assert.Same(t, tt.want, got)
		})
	}
}

// The answering model is only looked up when the routed model has no exact
// price, so a cache hit does not decode its body for the common case.
func TestResolveServedModelPricingSkipsAnsweredLookupForExactRoutedPrice(t *testing.T) {
	v := 1.0
	resolver := checkedPricingResolver{exact: map[string]*core.ModelPricing{"gpt-4o": {InputPerMtok: &v}}}
	calls := 0
	ResolveServedModelPricing(resolver, "gpt-4o", "openai", func() string {
		calls++
		return "gpt-4o-2024-08-06"
	})
	assert.Zero(t, calls)
}

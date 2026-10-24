package pricingoverrides

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

func windowedBasePricing() *core.ModelPricing {
	input, output, cached := 0.44, 1.32, 0.014
	offInput, offOutput, offCached := 0.22, 0.66, 0.007
	return &core.ModelPricing{
		Currency:           "USD",
		InputPerMtok:       &input,
		OutputPerMtok:      &output,
		CachedInputPerMtok: &cached,
		TimeWindows: []core.ModelPricingTimeWindow{{
			Label:     "off_peak",
			UTCRanges: []core.ModelPricingUTCRange{{Start: "10:00", End: "01:00"}},
			Pricing: core.ModelPricingTimeWindowRates{
				InputPerMtok:       &offInput,
				OutputPerMtok:      &offOutput,
				CachedInputPerMtok: &offCached,
			},
		}},
	}
}

func TestMergePricingKeepsCatalogTimeWindowsForUntouchedFields(t *testing.T) {
	perRequest := 0.01
	merged := mergePricing(windowedBasePricing(), Pricing{PerRequest: &perRequest})

	require.Len(t, merged.TimeWindows, 1)

	rates := merged.TimeWindows[0].Pricing
	require.NotNil(t, rates.InputPerMtok)
	require.Equal(t, 0.22, *rates.InputPerMtok)
	require.NotNil(t, rates.OutputPerMtok)
	require.NotNil(t, rates.CachedInputPerMtok, "window rates = %+v, want all catalog rates kept", rates)
}

func TestMergePricingDropsWindowRatesForOverriddenFields(t *testing.T) {
	input := 0.5
	merged := mergePricing(windowedBasePricing(), Pricing{InputPerMtok: &input})

	require.NotNil(t, merged.InputPerMtok)
	require.Equal(t, 0.5, *merged.InputPerMtok)
	require.Len(t, merged.TimeWindows, 1)

	rates := merged.TimeWindows[0].Pricing
	require.Nil(t, rates.InputPerMtok)
	require.NotNil(t, rates.OutputPerMtok)
	require.NotNil(t, rates.CachedInputPerMtok, "window rates = %+v, want output/cached rates kept", rates)

	output, cached := 2.0, 0.1
	merged = mergePricing(merged, Pricing{OutputPerMtok: &output, CachedInputPerMtok: &cached})
	require.Empty(t, merged.TimeWindows)
}

package providers

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

func windowedRegistryPricing() *core.ModelPricing {
	input, output := 0.44, 1.32
	offInput, offOutput := 0.22, 0.66
	return &core.ModelPricing{
		Currency:      "USD",
		InputPerMtok:  &input,
		OutputPerMtok: &output,
		TimeWindows: []core.ModelPricingTimeWindow{{
			Label:     "off_peak",
			UTCRanges: []core.ModelPricingUTCRange{{Start: "10:00", End: "01:00"}},
			Pricing:   core.ModelPricingTimeWindowRates{InputPerMtok: &offInput, OutputPerMtok: &offOutput},
		}},
	}
}

func TestMergeConfigPricingTimeWindows(t *testing.T) {
	t.Run("config override of a base rate drops the window rate for that field", func(t *testing.T) {
		input := 0.5
		merged := mergeConfigPricing(windowedRegistryPricing(), &core.ModelPricing{InputPerMtok: &input})
		require.Len(t, merged.TimeWindows, 1)

		rates := merged.TimeWindows[0].Pricing
		require.Nil(t, rates.InputPerMtok)
		require.NotNil(t, rates.OutputPerMtok)
		require.Equal(t, 0.66, *rates.OutputPerMtok, "window rates = %+v, want only the output rate kept", rates)
	})

	t.Run("config windows replace registry windows", func(t *testing.T) {
		custom := 0.1
		override := &core.ModelPricing{TimeWindows: []core.ModelPricingTimeWindow{{
			Label:     "negotiated",
			UTCRanges: []core.ModelPricingUTCRange{{Days: []string{"sat", "sun"}, Start: "00:00", End: "24:00"}},
			Pricing:   core.ModelPricingTimeWindowRates{InputPerMtok: &custom},
		}}}
		merged := mergeConfigPricing(windowedRegistryPricing(), override)
		require.Len(t, merged.TimeWindows, 1)
		require.Equal(t, "negotiated", merged.TimeWindows[0].Label)
		require.NotSame(t, override.TimeWindows[0].Pricing.InputPerMtok, merged.TimeWindows[0].Pricing.InputPerMtok)
		require.Equal(t, 0.44, *merged.InputPerMtok)
	})

	t.Run("no override keeps registry windows", func(t *testing.T) {
		merged := mergeConfigPricing(windowedRegistryPricing(), nil)
		require.Len(t, merged.TimeWindows, 1)
		require.NotNil(t, merged.TimeWindows[0].Pricing.InputPerMtok)
	})
}

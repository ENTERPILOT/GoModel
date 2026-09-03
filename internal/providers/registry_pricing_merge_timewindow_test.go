package providers

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
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
		if len(merged.TimeWindows) != 1 {
			t.Fatalf("TimeWindows = %d, want 1", len(merged.TimeWindows))
		}
		rates := merged.TimeWindows[0].Pricing
		if rates.InputPerMtok != nil || rates.OutputPerMtok == nil || *rates.OutputPerMtok != 0.66 {
			t.Fatalf("window rates = %+v, want only the output rate kept", rates)
		}
	})

	t.Run("config windows replace registry windows", func(t *testing.T) {
		custom := 0.1
		override := &core.ModelPricing{TimeWindows: []core.ModelPricingTimeWindow{{
			Label:     "negotiated",
			UTCRanges: []core.ModelPricingUTCRange{{Days: []string{"sat", "sun"}, Start: "00:00", End: "24:00"}},
			Pricing:   core.ModelPricingTimeWindowRates{InputPerMtok: &custom},
		}}}
		merged := mergeConfigPricing(windowedRegistryPricing(), override)
		if len(merged.TimeWindows) != 1 || merged.TimeWindows[0].Label != "negotiated" {
			t.Fatalf("TimeWindows = %+v, want the config window only", merged.TimeWindows)
		}
		if merged.TimeWindows[0].Pricing.InputPerMtok == override.TimeWindows[0].Pricing.InputPerMtok {
			t.Fatal("merged window shares the override's rate pointer, want a copy")
		}
		if *merged.InputPerMtok != 0.44 {
			t.Fatalf("InputPerMtok = %v, want registry base 0.44 kept", *merged.InputPerMtok)
		}
	})

	t.Run("no override keeps registry windows", func(t *testing.T) {
		merged := mergeConfigPricing(windowedRegistryPricing(), nil)
		if len(merged.TimeWindows) != 1 || merged.TimeWindows[0].Pricing.InputPerMtok == nil {
			t.Fatalf("TimeWindows = %+v, want registry window kept", merged.TimeWindows)
		}
	})
}

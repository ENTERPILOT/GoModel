package pricingoverrides

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
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

	if len(merged.TimeWindows) != 1 {
		t.Fatalf("TimeWindows = %d, want the catalog window kept", len(merged.TimeWindows))
	}
	rates := merged.TimeWindows[0].Pricing
	if rates.InputPerMtok == nil || *rates.InputPerMtok != 0.22 || rates.OutputPerMtok == nil || rates.CachedInputPerMtok == nil {
		t.Fatalf("window rates = %+v, want all catalog rates kept", rates)
	}
}

func TestMergePricingDropsWindowRatesForOverriddenFields(t *testing.T) {
	input := 0.5
	merged := mergePricing(windowedBasePricing(), Pricing{InputPerMtok: &input})

	if *merged.InputPerMtok != 0.5 {
		t.Fatalf("InputPerMtok = %v, want override 0.5", *merged.InputPerMtok)
	}
	if len(merged.TimeWindows) != 1 {
		t.Fatalf("TimeWindows = %d, want window kept for output/cached rates", len(merged.TimeWindows))
	}
	rates := merged.TimeWindows[0].Pricing
	if rates.InputPerMtok != nil {
		t.Fatalf("window InputPerMtok = %v, want dropped once the base input rate is overridden", *rates.InputPerMtok)
	}
	if rates.OutputPerMtok == nil || rates.CachedInputPerMtok == nil {
		t.Fatalf("window rates = %+v, want output/cached rates kept", rates)
	}

	output, cached := 2.0, 0.1
	merged = mergePricing(merged, Pricing{OutputPerMtok: &output, CachedInputPerMtok: &cached})
	if len(merged.TimeWindows) != 0 {
		t.Fatalf("TimeWindows = %+v, want none once every window rate is overridden", merged.TimeWindows)
	}
}

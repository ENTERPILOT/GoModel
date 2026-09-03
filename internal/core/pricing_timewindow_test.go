package core

import (
	"testing"
	"time"
)

// deepSeekPricing mirrors the ai-model-list entry for deepseek-v4-flash: base
// prices are the peak rates, the off-peak window halves them on weekdays
// outside 01:00-04:00 and 06:00-10:00 UTC and all day on weekends.
func deepSeekPricing() *ModelPricing {
	weekdays := []string{"mon", "tue", "wed", "thu", "fri"}
	return &ModelPricing{
		Currency:           "USD",
		InputPerMtok:       new(0.44),
		OutputPerMtok:      new(1.32),
		CachedInputPerMtok: new(0.014),
		TimeWindows: []ModelPricingTimeWindow{{
			Label: "off_peak",
			UTCRanges: []ModelPricingUTCRange{
				{Days: weekdays, Start: "00:00", End: "01:00"},
				{Days: weekdays, Start: "04:00", End: "06:00"},
				{Days: weekdays, Start: "10:00", End: "24:00"},
				{Days: []string{"sat", "sun"}, Start: "00:00", End: "24:00"},
			},
			Pricing: ModelPricingTimeWindowRates{
				InputPerMtok:       new(0.22),
				OutputPerMtok:      new(0.66),
				CachedInputPerMtok: new(0.007),
			},
		}},
	}
}

func TestModelPricingAtTime_DeepSeekSchedule(t *testing.T) {
	pricing := deepSeekPricing()
	// 2026-08-24 is a Monday.
	tests := []struct {
		name    string
		at      time.Time
		offPeak bool
	}{
		{"monday 00:30 before first peak", time.Date(2026, 8, 24, 0, 30, 0, 0, time.UTC), true},
		{"monday 01:00 peak start is inclusive", time.Date(2026, 8, 24, 1, 0, 0, 0, time.UTC), false},
		{"monday 03:59 peak", time.Date(2026, 8, 24, 3, 59, 0, 0, time.UTC), false},
		{"monday 04:00 off-peak gap start is inclusive", time.Date(2026, 8, 24, 4, 0, 0, 0, time.UTC), true},
		{"monday 05:59 off-peak gap", time.Date(2026, 8, 24, 5, 59, 0, 0, time.UTC), true},
		{"monday 06:00 second peak", time.Date(2026, 8, 24, 6, 0, 0, 0, time.UTC), false},
		{"monday 09:59 second peak", time.Date(2026, 8, 24, 9, 59, 0, 0, time.UTC), false},
		{"monday 10:00 evening off-peak", time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC), true},
		{"monday 23:59 evening off-peak", time.Date(2026, 8, 24, 23, 59, 0, 0, time.UTC), true},
		{"friday 08:00 peak", time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC), false},
		{"saturday 08:00 weekend off-peak", time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC), true},
		{"sunday 02:00 weekend off-peak", time.Date(2026, 8, 30, 2, 0, 0, 0, time.UTC), true},
		{"peak given in another zone is evaluated in UTC", time.Date(2026, 8, 24, 10, 0, 0, 0, time.FixedZone("CN", 8*3600)), false},
		{"off-peak given in another zone is evaluated in UTC", time.Date(2026, 8, 24, 19, 0, 0, 0, time.FixedZone("CN", 8*3600)), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := pricing.AtTime(tc.at)
			wantInput, wantOutput, wantCached := 0.44, 1.32, 0.014
			if tc.offPeak {
				wantInput, wantOutput, wantCached = 0.22, 0.66, 0.007
			}
			if *got.InputPerMtok != wantInput || *got.OutputPerMtok != wantOutput || *got.CachedInputPerMtok != wantCached {
				t.Fatalf("AtTime(%s) = in %v out %v cached %v, want in %v out %v cached %v",
					tc.at, *got.InputPerMtok, *got.OutputPerMtok, *got.CachedInputPerMtok, wantInput, wantOutput, wantCached)
			}
			if _, ok := got.TimeWindowAt(tc.at); ok != tc.offPeak {
				t.Fatalf("TimeWindowAt(%s) matched = %v, want %v", tc.at, ok, tc.offPeak)
			}
		})
	}
}

func TestModelPricingAtTime_LeavesBaseUntouched(t *testing.T) {
	pricing := deepSeekPricing()
	offPeak := pricing.AtTime(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	if offPeak == pricing {
		t.Fatal("AtTime returned the receiver for a matching window")
	}
	if *pricing.InputPerMtok != 0.44 || *pricing.OutputPerMtok != 1.32 {
		t.Fatalf("base pricing mutated: %v / %v", *pricing.InputPerMtok, *pricing.OutputPerMtok)
	}
	if pricing.CacheWritePerMtok != nil || offPeak.CacheWritePerMtok != nil {
		t.Fatal("window without cache_write_per_mtok must not introduce one")
	}
}

func TestModelPricingAtTime_PartialWindowKeepsOtherBaseRates(t *testing.T) {
	pricing := &ModelPricing{
		InputPerMtok:  new(1.0),
		OutputPerMtok: new(2.0),
		TimeWindows: []ModelPricingTimeWindow{{
			Label:     "discount",
			UTCRanges: []ModelPricingUTCRange{{Start: "00:00", End: "24:00"}},
			Pricing:   ModelPricingTimeWindowRates{InputPerMtok: new(0.5)},
		}},
	}
	got := pricing.AtTime(time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC))
	if *got.InputPerMtok != 0.5 || *got.OutputPerMtok != 2 {
		t.Fatalf("AtTime = in %v out %v, want in 0.5 out 2", *got.InputPerMtok, *got.OutputPerMtok)
	}
}

func TestModelPricingAtTime_ReturnsReceiverWithoutWindowsOrTime(t *testing.T) {
	var nilPricing *ModelPricing
	if nilPricing.AtTime(time.Now()) != nil {
		t.Fatal("nil receiver must stay nil")
	}
	plain := &ModelPricing{InputPerMtok: new(1.0)}
	if plain.AtTime(time.Now()) != plain {
		t.Fatal("pricing without windows must return the receiver")
	}
	windowed := deepSeekPricing()
	if windowed.AtTime(time.Time{}) != windowed {
		t.Fatal("zero time must price at the base rates")
	}
}

func TestModelPricingAtTime_FirstMatchingWindowWins(t *testing.T) {
	pricing := &ModelPricing{
		InputPerMtok: new(1.0),
		TimeWindows: []ModelPricingTimeWindow{
			{Label: "first", UTCRanges: []ModelPricingUTCRange{{Start: "10:00", End: "12:00"}}, Pricing: ModelPricingTimeWindowRates{InputPerMtok: new(0.1)}},
			{Label: "second", UTCRanges: []ModelPricingUTCRange{{Start: "00:00", End: "24:00"}}, Pricing: ModelPricingTimeWindowRates{InputPerMtok: new(0.2)}},
		},
	}
	if got := pricing.AtTime(time.Date(2026, 8, 24, 11, 0, 0, 0, time.UTC)); *got.InputPerMtok != 0.1 {
		t.Fatalf("overlap: got %v, want first window's 0.1", *got.InputPerMtok)
	}
	if got := pricing.AtTime(time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC)); *got.InputPerMtok != 0.2 {
		t.Fatalf("outside first: got %v, want second window's 0.2", *got.InputPerMtok)
	}
}

func TestModelPricingUTCRangeContains(t *testing.T) {
	monday := func(hour, minute int) time.Time { return time.Date(2026, 8, 24, hour, minute, 0, 0, time.UTC) }
	saturday := func(hour, minute int) time.Time { return time.Date(2026, 8, 29, hour, minute, 0, 0, time.UTC) }
	sunday := func(hour, minute int) time.Time { return time.Date(2026, 8, 30, hour, minute, 0, 0, time.UTC) }

	tests := []struct {
		name string
		r    ModelPricingUTCRange
		at   time.Time
		want bool
	}{
		{"plain range start inclusive", ModelPricingUTCRange{Start: "04:00", End: "06:00"}, monday(4, 0), true},
		{"plain range end exclusive", ModelPricingUTCRange{Start: "04:00", End: "06:00"}, monday(6, 0), false},
		{"wrap covers evening", ModelPricingUTCRange{Start: "10:00", End: "01:00"}, monday(23, 0), true},
		{"wrap covers early morning", ModelPricingUTCRange{Start: "10:00", End: "01:00"}, monday(0, 59), true},
		{"wrap end exclusive", ModelPricingUTCRange{Start: "10:00", End: "01:00"}, monday(1, 0), false},
		{"wrap excludes midday", ModelPricingUTCRange{Start: "10:00", End: "01:00"}, monday(5, 0), false},
		{"24:00 end runs to midnight", ModelPricingUTCRange{Start: "10:00", End: "24:00"}, monday(23, 59), true},
		{"24:00 end does not spill into next day", ModelPricingUTCRange{Start: "10:00", End: "24:00"}, monday(0, 0), false},
		{"equal bounds mean the whole day", ModelPricingUTCRange{Start: "00:00", End: "00:00"}, monday(12, 0), true},
		{"day restriction excludes other days", ModelPricingUTCRange{Days: []string{"sat", "sun"}, Start: "00:00", End: "24:00"}, monday(12, 0), false},
		{"day restriction includes listed day", ModelPricingUTCRange{Days: []string{"sat", "sun"}, Start: "00:00", End: "24:00"}, sunday(12, 0), true},
		{"wrapping range spills into the day after a listed day", ModelPricingUTCRange{Days: []string{"fri"}, Start: "10:00", End: "01:00"}, saturday(0, 30), true},
		{"wrapping range does not start on an unlisted day", ModelPricingUTCRange{Days: []string{"fri"}, Start: "10:00", End: "01:00"}, saturday(12, 0), false},
		{"full day names and mixed case are accepted", ModelPricingUTCRange{Days: []string{"Monday"}, Start: "00:00", End: "24:00"}, monday(12, 0), true},
		{"unknown day never matches", ModelPricingUTCRange{Days: []string{"someday"}, Start: "00:00", End: "24:00"}, monday(12, 0), false},
		{"malformed start never matches", ModelPricingUTCRange{Start: "25:00", End: "01:00"}, monday(12, 0), false},
		{"malformed end never matches", ModelPricingUTCRange{Start: "01:00", End: "1:00"}, monday(12, 0), false},
		{"non-UTC time is converted", ModelPricingUTCRange{Start: "04:00", End: "06:00"}, time.Date(2026, 8, 24, 13, 0, 0, 0, time.FixedZone("CN", 8*3600)), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.r.Contains(tc.at); got != tc.want {
				t.Fatalf("Contains(%s) = %v, want %v", tc.at, got, tc.want)
			}
		})
	}
}

func TestModelPricingDropTimeWindowRatesOverriddenBy(t *testing.T) {
	pricing := deepSeekPricing()
	pricing.DropTimeWindowRatesOverriddenBy(&ModelPricing{InputPerMtok: new(9.0)})
	if len(pricing.TimeWindows) != 1 {
		t.Fatalf("windows = %d, want 1 (output and cached rates survive)", len(pricing.TimeWindows))
	}
	rates := pricing.TimeWindows[0].Pricing
	if rates.InputPerMtok != nil || rates.OutputPerMtok == nil || rates.CachedInputPerMtok == nil {
		t.Fatalf("rates after input override = %+v", rates)
	}

	pricing.DropTimeWindowRatesOverriddenBy(&ModelPricing{OutputPerMtok: new(9.0), CachedInputPerMtok: new(9.0)})
	if pricing.TimeWindows != nil {
		t.Fatalf("window with no rates left must be dropped, got %+v", pricing.TimeWindows)
	}

	untouched := deepSeekPricing()
	untouched.DropTimeWindowRatesOverriddenBy(&ModelPricing{PerRequest: new(1.0)})
	if len(untouched.TimeWindows) != 1 || untouched.TimeWindows[0].Pricing.InputPerMtok == nil {
		t.Fatal("override of an unrelated field must keep window rates")
	}
}

func TestModelPricingClone_DeepCopiesTimeWindows(t *testing.T) {
	pricing := deepSeekPricing()
	clone := pricing.Clone()
	if len(clone.TimeWindows) != 1 || clone.TimeWindows[0].Pricing.InputPerMtok == pricing.TimeWindows[0].Pricing.InputPerMtok {
		t.Fatal("Clone must re-allocate window rate pointers")
	}
	clone.TimeWindows[0].UTCRanges[0].Days[0] = "changed"
	*clone.TimeWindows[0].Pricing.InputPerMtok = 99
	if pricing.TimeWindows[0].UTCRanges[0].Days[0] != "mon" || *pricing.TimeWindows[0].Pricing.InputPerMtok != 0.22 {
		t.Fatal("mutating the clone leaked into the original")
	}
	if sources := pricing.FieldSources("model_registry"); sources["time_windows"] != "model_registry" {
		t.Fatalf("FieldSources = %v, want time_windows reported", sources)
	}
}

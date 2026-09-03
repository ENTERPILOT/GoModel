package core

import (
	"strings"
	"time"
)

// ModelPricingTimeWindow is a recurring window during which some per-token
// rates replace the base prices — DeepSeek's off-peak hours, for example. The
// base prices stay the standard (peak) rates, so a consumer that ignores the
// windows never understates cost.
type ModelPricingTimeWindow struct {
	Label     string                      `json:"label" yaml:"label"`
	UTCRanges []ModelPricingUTCRange      `json:"utc_ranges" yaml:"utc_ranges"`
	Pricing   ModelPricingTimeWindowRates `json:"pricing" yaml:"pricing"`
}

// ModelPricingUTCRange is a half-open daily UTC range [start, end). An end at
// or before the start wraps past midnight into the next day. Days optionally
// limits the range to weekdays ("mon" … "sun"); a wrapping range starts on a
// listed day and spills into the following one.
type ModelPricingUTCRange struct {
	Days  []string `json:"days,omitempty" yaml:"days,omitempty"`
	Start string   `json:"start" yaml:"start"`
	End   string   `json:"end" yaml:"end"`
}

// ModelPricingTimeWindowRates are the base fields a time window can replace.
// Absent fields keep their base price.
type ModelPricingTimeWindowRates struct {
	InputPerMtok       *float64 `json:"input_per_mtok,omitempty" yaml:"input_per_mtok,omitempty"`
	OutputPerMtok      *float64 `json:"output_per_mtok,omitempty" yaml:"output_per_mtok,omitempty"`
	CachedInputPerMtok *float64 `json:"cached_input_per_mtok,omitempty" yaml:"cached_input_per_mtok,omitempty"`
	CacheWritePerMtok  *float64 `json:"cache_write_per_mtok,omitempty" yaml:"cache_write_per_mtok,omitempty"`
}

const minutesPerDay = 24 * 60

var weekdayNames = map[string]time.Weekday{
	"mon": time.Monday,
	"tue": time.Tuesday,
	"wed": time.Wednesday,
	"thu": time.Thursday,
	"fri": time.Friday,
	"sat": time.Saturday,
	"sun": time.Sunday,
}

// AtTime returns the pricing in effect at t: the first time window containing
// t (evaluated in UTC) replaces the base rates it publishes. Without windows,
// without a match, or for a zero t the receiver itself is returned, so the
// standard rates always apply when the moment of use is unknown.
func (p *ModelPricing) AtTime(t time.Time) *ModelPricing {
	if p == nil || len(p.TimeWindows) == 0 || t.IsZero() {
		return p
	}
	window, ok := p.TimeWindowAt(t)
	if !ok {
		return p
	}
	effective := *p
	rates := window.Pricing
	if rates.InputPerMtok != nil {
		effective.InputPerMtok = rates.InputPerMtok
	}
	if rates.OutputPerMtok != nil {
		effective.OutputPerMtok = rates.OutputPerMtok
	}
	if rates.CachedInputPerMtok != nil {
		effective.CachedInputPerMtok = rates.CachedInputPerMtok
	}
	if rates.CacheWritePerMtok != nil {
		effective.CacheWritePerMtok = rates.CacheWritePerMtok
	}
	return &effective
}

// TimeWindowAt returns the first time window containing t, if any.
func (p *ModelPricing) TimeWindowAt(t time.Time) (ModelPricingTimeWindow, bool) {
	if p == nil {
		return ModelPricingTimeWindow{}, false
	}
	for _, window := range p.TimeWindows {
		if window.Contains(t) {
			return window, true
		}
	}
	return ModelPricingTimeWindow{}, false
}

// Contains reports whether t (evaluated in UTC) falls in any of the window's ranges.
func (w ModelPricingTimeWindow) Contains(t time.Time) bool {
	for _, r := range w.UTCRanges {
		if r.Contains(t) {
			return true
		}
	}
	return false
}

// Contains reports whether t (evaluated in UTC) falls in the range. A range
// with an unparseable bound never matches, so malformed catalog data falls
// back to the base rates rather than a discount.
func (r ModelPricingUTCRange) Contains(t time.Time) bool {
	start, ok := parseClockMinutes(r.Start)
	if !ok {
		return false
	}
	end, ok := parseClockMinutes(r.End)
	if !ok {
		return false
	}
	utc := t.UTC()
	minute := utc.Hour()*60 + utc.Minute()
	day := utc.Weekday()
	if end > start {
		return r.onDay(day) && minute >= start && minute < end
	}
	// Wrapping range: [start, 24:00) on a listed day, then [00:00, end) on the
	// day after it.
	if r.onDay(day) && minute >= start {
		return true
	}
	return r.onDay((day+6)%7) && minute < end
}

func (r ModelPricingUTCRange) onDay(day time.Weekday) bool {
	if len(r.Days) == 0 {
		return true
	}
	for _, name := range r.Days {
		if weekday, ok := parseWeekday(name); ok && weekday == day {
			return true
		}
	}
	return false
}

// parseWeekday accepts the three-letter forms "mon" … "sun" and full English
// names, case-insensitively. Anything else (including other prefixes such as
// "monda") is rejected so a catalog typo cannot activate a window.
func parseWeekday(name string) (time.Weekday, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if len(name) < 3 {
		return 0, false
	}
	weekday, ok := weekdayNames[name[:3]]
	if !ok {
		return 0, false
	}
	if len(name) > 3 && name != strings.ToLower(weekday.String()) {
		return 0, false
	}
	return weekday, true
}

// parseClockMinutes parses "HH:MM" into minutes since midnight. "24:00" is
// accepted as an end-of-day bound and maps to 0, which the caller treats as a
// wrap that covers the rest of the day.
func parseClockMinutes(value string) (int, bool) {
	hh, mm, ok := strings.Cut(strings.TrimSpace(value), ":")
	if !ok || len(hh) != 2 || len(mm) != 2 {
		return 0, false
	}
	hour, ok := twoDigits(hh)
	if !ok {
		return 0, false
	}
	minute, ok := twoDigits(mm)
	if !ok || minute > 59 {
		return 0, false
	}
	if hour > 24 || (hour == 24 && minute != 0) {
		return 0, false
	}
	return (hour*60 + minute) % minutesPerDay, true
}

func twoDigits(s string) (int, bool) {
	if s[0] < '0' || s[0] > '9' || s[1] < '0' || s[1] > '9' {
		return 0, false
	}
	return int(s[0]-'0')*10 + int(s[1]-'0'), true
}

// DropTimeWindowRatesOverriddenBy removes from every time window the rates
// whose base field the override sets, so an operator's rate is not undercut
// by a catalog discount that was published against a different base price.
// Windows left without any rate are dropped.
func (p *ModelPricing) DropTimeWindowRatesOverriddenBy(override *ModelPricing) {
	if p == nil || override == nil || len(p.TimeWindows) == 0 {
		return
	}
	kept := make([]ModelPricingTimeWindow, 0, len(p.TimeWindows))
	for _, window := range p.TimeWindows {
		rates := window.Pricing
		if override.InputPerMtok != nil {
			rates.InputPerMtok = nil
		}
		if override.OutputPerMtok != nil {
			rates.OutputPerMtok = nil
		}
		if override.CachedInputPerMtok != nil {
			rates.CachedInputPerMtok = nil
		}
		if override.CacheWritePerMtok != nil {
			rates.CacheWritePerMtok = nil
		}
		if rates == (ModelPricingTimeWindowRates{}) {
			continue
		}
		window.Pricing = rates
		kept = append(kept, window)
	}
	if len(kept) == 0 {
		kept = nil
	}
	p.TimeWindows = kept
}

func cloneTimeWindows(windows []ModelPricingTimeWindow) []ModelPricingTimeWindow {
	if len(windows) == 0 {
		return nil
	}
	out := make([]ModelPricingTimeWindow, len(windows))
	for i, w := range windows {
		// Preserve nil versus empty: utc_ranges has no omitempty, so the
		// distinction is visible in serialized metadata.
		var ranges []ModelPricingUTCRange
		if w.UTCRanges != nil {
			ranges = make([]ModelPricingUTCRange, len(w.UTCRanges))
		}
		for j, r := range w.UTCRanges {
			ranges[j] = ModelPricingUTCRange{Start: r.Start, End: r.End}
			if len(r.Days) > 0 {
				ranges[j].Days = append([]string(nil), r.Days...)
			}
		}
		out[i] = ModelPricingTimeWindow{
			Label:     w.Label,
			UTCRanges: ranges,
			Pricing: ModelPricingTimeWindowRates{
				InputPerMtok:       cloneFloatPtr(w.Pricing.InputPerMtok),
				OutputPerMtok:      cloneFloatPtr(w.Pricing.OutputPerMtok),
				CachedInputPerMtok: cloneFloatPtr(w.Pricing.CachedInputPerMtok),
				CacheWritePerMtok:  cloneFloatPtr(w.Pricing.CacheWritePerMtok),
			},
		}
	}
	return out
}

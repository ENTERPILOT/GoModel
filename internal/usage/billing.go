package usage

import (
	"slices"
	"strings"
)

// BilledCost returns what the caller is billed for a request that cost
// totalCost upstream: the cost plus markupPercent, or zero for a response-cache
// hit that is not billed, even when its cost is unknown. Otherwise a nil
// totalCost (unknown cost) stays nil; a nil markupPercent means no markup.
func BilledCost(totalCost, markupPercent *float64, cacheHit, billCacheHits bool) *float64 {
	if cacheHit && !billCacheHits {
		return new(0.0)
	}
	if totalCost == nil {
		return nil
	}
	billed := *totalCost
	if markupPercent != nil {
		billed *= 1 + *markupPercent/100
	}
	return &billed
}

// addCost sums two possibly unknown costs: the result is nil only when both
// are nil, matching SQL SUM over rows whose costs may be NULL.
func addCost(a, b *float64) *float64 {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	sum := *a + *b
	return &sum
}

// billedCacheHitsMode returns the cache mode that selects the response-cache
// hits a query's billed totals still have to add, or "" when its aggregates
// already include them. Billed totals cover every billed row, so an
// uncached-mode query adds the cache hits its cost sums exclude.
func billedCacheHitsMode(mode string) string {
	if normalizeCacheMode(mode) == CacheModeUncached {
		return CacheModeCached
	}
	return ""
}

// addPeriodBilledCost adds each period's billed cache hits to the daily
// series, appending a row for a period billed only from the cache, and keeps
// the series in period order.
func addPeriodBilledCost(daily []DailyUsage, hits map[string]*float64) []DailyUsage {
	seen := make(map[string]bool, len(daily))
	for i := range daily {
		seen[daily[i].Date] = true
		daily[i].BilledCost = addCost(daily[i].BilledCost, hits[daily[i].Date])
	}
	added := false
	for period, billed := range hits {
		if seen[period] || billed == nil || *billed == 0 {
			continue
		}
		daily = append(daily, DailyUsage{Date: period, BilledCost: billed})
		added = true
	}
	if added {
		slices.SortFunc(daily, func(a, b DailyUsage) int { return strings.Compare(a.Date, b.Date) })
	}
	return daily
}

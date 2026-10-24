package usage

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

package usage

// BilledCost returns what the caller is billed for a request that cost
// totalCost upstream: the cost plus markupPercent, or zero for a response-cache
// hit that is not billed. A nil totalCost (unknown cost) stays nil; a nil
// markupPercent means no markup.
func BilledCost(totalCost, markupPercent *float64, cacheHit, billCacheHits bool) *float64 {
	if totalCost == nil {
		return nil
	}
	billed := 0.0
	if !cacheHit || billCacheHits {
		billed = *totalCost
		if markupPercent != nil {
			billed *= 1 + *markupPercent/100
		}
	}
	return &billed
}

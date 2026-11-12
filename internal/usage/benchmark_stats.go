package usage

import (
	"math"
	"slices"
)

// costStats summarizes per-user costs. Percentiles use linear interpolation
// between closest ranks (Hyndman and Fan type 7: Excel PERCENTILE.INC, NumPy's
// default, PostgreSQL percentile_cont): the p-th percentile of n sorted values
// sits at the 0-based rank h = (n-1)*p, between values floor(h) and ceil(h).
// So the median of an even count is the mean of the two middle values, and a
// single value is every percentile.
type costStats struct {
	Mean, P50, P90, Max float64
}

// computeCostStats returns the statistics of costs, which it sorts in place.
// It reports false for no costs.
func computeCostStats(costs []float64) (costStats, bool) {
	if len(costs) == 0 {
		return costStats{}, false
	}
	slices.Sort(costs)
	sum := 0.0
	for _, cost := range costs {
		sum += cost
	}
	return costStats{
		Mean: sum / float64(len(costs)),
		P50:  percentile(costs, 0.5),
		P90:  percentile(costs, 0.9),
		Max:  costs[len(costs)-1],
	}, true
}

// percentile interpolates the p-th quantile (0 <= p <= 1) of sorted, a
// non-empty ascending slice.
func percentile(sorted []float64, p float64) float64 {
	rank := float64(len(sorted)-1) * p
	lower := int(math.Floor(rank))
	upper := int(math.Ceil(rank))
	return sorted[lower] + (sorted[upper]-sorted[lower])*(rank-float64(lower))
}

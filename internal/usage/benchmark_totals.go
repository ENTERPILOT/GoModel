package usage

import (
	"fmt"

	"github.com/enterpilot/gomodel/internal/core"
)

type benchmarkKey struct{ month, user string }

// benchmarkUserMonth is one user's month: activity from every request, and
// each cost basis summed the way the invoice sums it.
type benchmarkUserMonth struct {
	providerRequests int
	requests         int
	providerCost     *float64
	billedCost       *float64
	// providerUnpriced and billedUnpriced report a request whose cost the
	// basis includes but does not know.
	providerUnpriced bool
	billedUnpriced   bool
}

// cost returns the user's cost on basis and whether it is wholly or partly
// unknown. A user with no provider requests costs a known 0 on either basis
// when nothing billed it, as the invoice leaves it out.
func (u *benchmarkUserMonth) cost(basis string) (cost float64, unpriced, partly bool) {
	sum, missing := u.billedCost, u.billedUnpriced
	if basis == BenchmarkCostBasisProvider {
		sum, missing = u.providerCost, u.providerUnpriced
	}
	if sum == nil {
		return 0, u.providerRequests > 0, false
	}
	return *sum, false, missing
}

// benchmarkTotals sums storage groups into (month, user) totals; usage on
// the parent itself is kept under the parent's own key.
type benchmarkTotals struct {
	parent string
	users  map[benchmarkKey]*benchmarkUserMonth
}

func (b benchmarkTotals) user(group breakdownGroup) (*benchmarkUserMonth, error) {
	key := benchmarkKey{month: group.Column, user: invoiceCustomer(b.parent, group.Row)}
	totals, ok := b.users[key]
	if ok {
		return totals, nil
	}
	if len(b.users) >= maxBenchmarkUserMonths {
		return nil, core.NewInvalidRequestError(fmt.Sprintf(
			"benchmark has more than %d user months; pick a deeper parent, fewer months or narrower filters", maxBenchmarkUserMonths), nil)
	}
	totals = &benchmarkUserMonth{}
	b.users[key] = totals
	return totals, nil
}

// addProvider adds a group of provider-bound requests, which count toward
// requests and both cost bases.
func (b benchmarkTotals) addProvider(group breakdownGroup) error {
	totals, err := b.user(group)
	if err != nil {
		return err
	}
	totals.requests += group.Requests
	totals.providerRequests += group.Requests
	totals.providerCost = addCost(totals.providerCost, group.TotalCost)
	totals.billedCost = addCost(totals.billedCost, group.BilledCost)
	totals.providerUnpriced = totals.providerUnpriced || group.PricedRequests < group.Requests
	totals.billedUnpriced = totals.billedUnpriced || group.BilledPricedRequests < group.Requests
	return nil
}

// addCacheHit adds a group of response-cache hits: they make the user active,
// and only what billedHitGroup keeps of them reaches billed cost, exactly as
// on the invoice.
func (b benchmarkTotals) addCacheHit(group breakdownGroup) error {
	totals, err := b.user(group)
	if err != nil {
		return err
	}
	totals.requests += group.Requests
	if billed, ok := billedHitGroup(group); ok {
		totals.billedCost = addCost(totals.billedCost, billed.BilledCost)
		totals.billedUnpriced = totals.billedUnpriced || group.BilledPricedRequests < group.Requests
	}
	return nil
}

// fill computes each month's statistics into benchmark.
func (b benchmarkTotals) fill(benchmark *Benchmark, params BenchmarkParams) {
	users := map[string][]BenchmarkUser{}
	unattributed := map[string]float64{}
	unpriced := map[string]int{}
	partly := map[string]int{}
	for key, totals := range b.users {
		if totals.requests == 0 {
			continue
		}
		cost, isUnpriced, isPartly := totals.cost(params.CostBasis)
		if key.user == b.parent {
			unattributed[key.month] += cost
			continue
		}
		if isUnpriced {
			unpriced[key.month]++
		}
		if isPartly {
			partly[key.month]++
		}
		users[key.month] = append(users[key.month], BenchmarkUser{User: key.user, Requests: totals.providerRequests, Cost: cost})
	}

	var previous *BenchmarkMonth
	for i := range benchmark.Months {
		month := &benchmark.Months[i]
		month.UnattributedCost = roundBenchmarkCost(unattributed[month.Month])
		month.UnpricedUsers = unpriced[month.Month]
		month.PartlyPricedUsers = partly[month.Month]
		active := users[month.Month]
		month.ActiveUsers = len(active)
		costs := make([]float64, len(active))
		for j, user := range active {
			costs[j] = user.Cost
			month.TotalCost += user.Cost
		}
		month.TotalCost = roundBenchmarkCost(month.TotalCost)
		if stats, ok := computeCostStats(costs); ok {
			month.Mean = new(roundBenchmarkCost(stats.Mean))
			month.P50 = new(roundBenchmarkCost(stats.P50))
			month.P90 = new(roundBenchmarkCost(stats.P90))
			month.Max = new(roundBenchmarkCost(stats.Max))
		}
		// A partial month set against a complete one reads as a drop.
		if previous != nil && !month.Partial {
			month.MeanChangePercent = changePercent(previous.Mean, month.Mean)
			month.P50ChangePercent = changePercent(previous.P50, month.P50)
		}
		if params.TopUsers > 0 {
			month.TopUsers = topBenchmarkUsers(active, params.TopUsers)
		}
		previous = month
	}
}

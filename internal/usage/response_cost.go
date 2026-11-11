package usage

import (
	"context"
	"math"
	"sync"

	"github.com/enterpilot/gomodel/internal/core"
)

// ResponseCostCurrency is the currency of every cost GoModel calculates.
const ResponseCostCurrency = "USD"

// ResponseCost returns what the request recorded by entry cost its caller, or
// false when that is unknown. With billed set (markups enabled) it is the
// billed cost and nothing else: a reseller's customer never sees the provider
// cost. Otherwise it is the provider cost, which is zero for a response-cache
// hit because no provider was called.
func ResponseCost(entry *UsageEntry, billed bool) (float64, bool) {
	if entry == nil {
		return 0, false
	}
	cost := entry.TotalCost
	switch {
	case billed:
		cost = entry.BilledCost
	case entry.CacheType != "":
		return 0, true
	}
	if cost == nil || math.IsNaN(*cost) || math.IsInf(*cost, 0) || *cost < 0 {
		return 0, false
	}
	return *cost, true
}

// BillingStamper is implemented by the usage loggers that stamp billing fields
// (the billable flag, the markup and the billed cost) on an entry before they
// record it.
type BillingStamper interface {
	StampBilling(entry *UsageEntry)
}

// StampBilling stamps on entry the billing fields logger.Write would, without
// recording it, so the cost of a request can be known before its usage is
// written. Stamping is idempotent: Write stamps the same values again.
func StampBilling(logger LoggerInterface, entry *UsageEntry) {
	if stamper, ok := logger.(BillingStamper); ok && entry != nil {
		stamper.StampBilling(entry)
	}
}

// CostReport carries the cost of the request being served from the code that
// records its usage to the code that writes its response. It is safe for
// concurrent use; the latest recorded entry wins.
type CostReport struct {
	billed bool

	mu     sync.Mutex
	amount float64
	known  bool
}

type costReportKey struct{}

// WithCostReport attaches a new cost report to ctx. billed selects the billed
// cost over the provider cost; see ResponseCost.
func WithCostReport(ctx context.Context, billed bool) (context.Context, *CostReport) {
	report := &CostReport{billed: billed}
	return context.WithValue(ctx, costReportKey{}, report), report
}

// CostReportFromContext returns the cost report of the client request served
// under ctx, or nil when it has none: response costs are off, or ctx belongs
// to a model call the gateway makes itself (a guardrail, a plugin), whose
// usage is its own row and is not the cost of the client's response.
func CostReportFromContext(ctx context.Context) *CostReport {
	if ctx == nil || core.GetRequestOrigin(ctx) != core.RequestOriginExternal {
		return nil
	}
	report, _ := ctx.Value(costReportKey{}).(*CostReport)
	return report
}

// ReportCost records the cost of entry on the cost report of ctx, if any.
// Call it before logger.Write(entry): the entry must not be shared yet.
func ReportCost(ctx context.Context, logger LoggerInterface, entry *UsageEntry) {
	CostReportFromContext(ctx).Record(logger, entry)
}

// Record stamps entry's billing fields and keeps its cost as the cost of the
// request. A nil report records nothing.
func (r *CostReport) Record(logger LoggerInterface, entry *UsageEntry) {
	if r == nil || entry == nil {
		return
	}
	StampBilling(logger, entry)
	amount, known := ResponseCost(entry, r.billed)
	r.mu.Lock()
	r.amount, r.known = amount, known
	r.mu.Unlock()
}

// Cost returns the recorded cost, or false while it is unknown.
func (r *CostReport) Cost() (float64, bool) {
	if r == nil {
		return 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.amount, r.known
}

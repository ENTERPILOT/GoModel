package usage

import (
	"context"
	"fmt"
	"log/slog"
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
//
// A request whose pricing is unknown has no cost in either mode, even where
// its bill is fixed at zero (a billable: false provider, an unbilled cache
// hit): 0 always means a priced request that cost nothing.
func ResponseCost(entry *UsageEntry, billed bool) (float64, bool) {
	if entry == nil || !validCost(entry.TotalCost) {
		return 0, false
	}
	switch {
	case billed:
		if !validCost(entry.BilledCost) {
			return 0, false
		}
		return *entry.BilledCost, true
	case entry.CacheType != "":
		return 0, true
	}
	return *entry.TotalCost, true
}

func validCost(cost *float64) bool {
	return cost != nil && !math.IsNaN(*cost) && !math.IsInf(*cost, 0) && *cost >= 0
}

// BillingStamper is implemented by the usage loggers that stamp billing fields
// (the billable flag, the markup and the billed cost) on an entry before they
// record it.
type BillingStamper interface {
	StampBilling(entry *UsageEntry)
}

// StampBilling stamps on entry the billing fields logger.Write would, without
// recording it, so the cost of a request can be known before its usage is
// written. Stamping is idempotent: Write stamps the same values again, with
// the billability decided here (see BillableTap.StampBilling). A
// logger that wraps another must implement BillingStamper and forward it, or
// the billed cost is not known before the response; see CostReport.Record.
func StampBilling(logger LoggerInterface, entry *UsageEntry) {
	if stamper, ok := logger.(BillingStamper); ok && entry != nil {
		stamper.StampBilling(entry)
	}
}

// CostReport carries the cost of the request being served from the code that
// records its usage to the code that writes its response. It is safe for
// concurrent use; the latest recorded entry wins.
//
// A stream delivered later than it is read (streaming.NewSlowdownStream) has
// its usage recorded ahead of the events the response writes. Such a stream
// captures the cost after each chunk it reads and restores it when it
// delivers the chunk, and from the first capture on Cost returns the cost
// restored last: the cost as of the events delivered, not of the ones read.
type CostReport struct {
	billed bool

	mu        sync.Mutex
	recorded  reportedCost
	delivered reportedCost
	delayed   bool // Cost returns delivered
}

// reportedCost is a cost, or none while it is not known.
type reportedCost struct {
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
	if r.billed && validCost(entry.TotalCost) && entry.BilledCost == nil {
		// The markup tap bills every priced entry, so a priced entry without
		// a billed cost means a logger wrapper between here and that tap
		// does not forward StampBilling. The response carries no cost rather
		// than the provider cost, and the gap is reported, not hidden. It is
		// a fault of the logger chain, the same for every request, so no
		// request value is logged.
		slog.Error("response cost withheld: the usage logger did not stamp the billed cost",
			"logger", fmt.Sprintf("%T", logger),
			"hint", "every usage logger wrapper must implement usage.BillingStamper and forward it to the logger it wraps")
	}
	amount, known := ResponseCost(entry, r.billed)
	r.mu.Lock()
	r.recorded = reportedCost{amount: amount, known: known}
	r.mu.Unlock()
}

// Forget makes the cost unknown again. A stream reports the cost of each of
// its events on its own, so the cost of one event is never taken for the cost
// of another whose usage was not recorded. A nil report forgets nothing.
func (r *CostReport) Forget() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.recorded = reportedCost{}
	r.mu.Unlock()
}

// Cost returns the recorded cost, or false while it is unknown.
func (r *CostReport) Cost() (float64, bool) {
	if r == nil {
		return 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cost := r.recorded
	if r.delayed {
		cost = r.delivered
	}
	return cost.amount, cost.known
}

// Capture returns the recorded cost for a stream that delivers what it reads
// later, and makes Cost return what Restore was given last, nothing until
// then. It implements streaming.DeliveryState.
func (r *CostReport) Capture() any {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.delayed = true
	return r.recorded
}

// Restore makes a captured cost the one Cost returns.
func (r *CostReport) Restore(state any) {
	cost, _ := state.(reportedCost)
	r.mu.Lock()
	r.delivered = cost
	r.mu.Unlock()
}

package usage

import "strings"

// BillableTap decorates a usage logger so every entry served by a provider
// configured with billable: false is marked non-billable before it is
// recorded. All modalities and response-cache hits funnel through
// Logger.Write, so one tap covers them all. It wraps the markup tap, which
// bills a non-billable entry at zero.
type BillableTap struct {
	inner    LoggerInterface
	billable func(providerName string) bool
}

// NewBillableTap wraps the logger, or returns it unchanged when there is no
// provider lookup.
func NewBillableTap(inner LoggerInterface, billable func(providerName string) bool) LoggerInterface {
	if inner == nil || billable == nil {
		return inner
	}
	return &BillableTap{inner: inner, billable: billable}
}

func (t *BillableTap) Write(entry *UsageEntry) {
	t.mark(entry)
	t.inner.Write(entry)
}

// StampBilling implements BillingStamper.
func (t *BillableTap) StampBilling(entry *UsageEntry) {
	t.mark(entry)
	StampBilling(t.inner, entry)
}

func (t *BillableTap) mark(entry *UsageEntry) {
	if entry == nil {
		return
	}
	// Rows written before provider names existed carry only the provider
	// type, which is also the default instance name.
	name := strings.TrimSpace(entry.ProviderName)
	if name == "" {
		name = strings.TrimSpace(entry.Provider)
	}
	if name != "" && !t.billable(name) {
		entry.MarkNonBillable()
	}
}

// SetLivePublisher forwards the dashboard publisher to the wrapped logger, so
// wrapping it never silences live usage events.
func (t *BillableTap) SetLivePublisher(p LiveEventPublisher) {
	if logger, ok := t.inner.(interface {
		SetLivePublisher(LiveEventPublisher)
	}); ok {
		logger.SetLivePublisher(p)
	}
}

func (t *BillableTap) Config() Config {
	return t.inner.Config()
}

func (t *BillableTap) Close() error {
	return t.inner.Close()
}

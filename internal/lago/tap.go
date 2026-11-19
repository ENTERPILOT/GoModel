package lago

import "github.com/enterpilot/gomodel/internal/usage"

// Tap decorates a usage logger so every recorded entry is also billed in
// Lago. All modalities funnel through Logger.Write once the entry is final,
// at the end of the response or at stream close, so one tap covers them all.
type Tap struct {
	inner    usage.LoggerInterface
	exporter *Exporter
}

// NewTap wraps the logger. Wrap it before the markup and billable taps, so
// an entry is already marked non-billable when it gets here.
func NewTap(inner usage.LoggerInterface, exporter *Exporter) usage.LoggerInterface {
	if inner == nil || exporter == nil {
		return inner
	}
	return &Tap{inner: inner, exporter: exporter}
}

func (t *Tap) Write(entry *usage.UsageEntry) {
	t.exporter.Record(entry)
	t.inner.Write(entry)
}

// SetLivePublisher forwards the dashboard publisher to the wrapped logger, so
// wrapping it never silences live usage events.
func (t *Tap) SetLivePublisher(p usage.LiveEventPublisher) {
	if logger, ok := t.inner.(interface {
		SetLivePublisher(usage.LiveEventPublisher)
	}); ok {
		logger.SetLivePublisher(p)
	}
}

func (t *Tap) Config() usage.Config {
	return t.inner.Config()
}

// Close stores the buffered usage first, then sends Lago what is still
// waiting.
func (t *Tap) Close() error {
	err := t.inner.Close()
	t.exporter.Close()
	return err
}

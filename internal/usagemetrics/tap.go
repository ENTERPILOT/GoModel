// Package usagemetrics turns recorded usage entries into token and cost
// metrics. It reads each entry once and hands the result to the enabled
// metric pipelines (Prometheus, OpenTelemetry); it never touches storage.
package usagemetrics

import "github.com/enterpilot/gomodel/internal/usage"

// Recorder is one metric pipeline's sink for usage samples. RecordUsage runs
// on the request path and must not block.
type Recorder interface {
	RecordUsage(sample Sample)
}

// Tap decorates a usage logger so every recorded entry also feeds the token
// and cost metrics. All modalities and response-cache hits funnel through
// Logger.Write once the entry is final — at the end of the response, or at
// stream close — so one tap covers them all.
type Tap struct {
	inner     usage.LoggerInterface
	callers   *Callers
	recorders []Recorder
}

// NewTap wraps the logger, or returns it unchanged when no metric pipeline
// takes usage samples. Wrap the logger before the markup tap, so the entry
// already carries its billed cost when it gets here.
func NewTap(inner usage.LoggerInterface, callers *Callers, recorders ...Recorder) usage.LoggerInterface {
	if inner == nil || len(recorders) == 0 {
		return inner
	}
	return &Tap{inner: inner, callers: callers, recorders: recorders}
}

func (t *Tap) Write(entry *usage.UsageEntry) {
	if entry != nil {
		if sample := newSample(entry); !sample.empty() {
			sample.Caller = t.callers.Resolve(entry.UserPath, entry.AuthKeyID, entry.Labels)
			for _, recorder := range t.recorders {
				recorder.RecordUsage(sample)
			}
		}
	}
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

func (t *Tap) Close() error {
	return t.inner.Close()
}

package markups

import "github.com/enterpilot/gomodel/internal/usage"

// UsageTap decorates a usage logger so every recorded entry carries its
// billed cost. All modalities and response-cache hits funnel through
// Logger.Write, so one tap covers them all.
type UsageTap struct {
	inner   usage.LoggerInterface
	service *Service
}

// NewUsageTap wraps the logger, or returns it unchanged when there is no
// markup service.
func NewUsageTap(inner usage.LoggerInterface, service *Service) usage.LoggerInterface {
	if inner == nil || service == nil {
		return inner
	}
	return &UsageTap{inner: inner, service: service}
}

func (t *UsageTap) Write(entry *usage.UsageEntry) {
	t.service.Apply(entry)
	t.inner.Write(entry)
}

func (t *UsageTap) Config() usage.Config {
	return t.inner.Config()
}

func (t *UsageTap) Close() error {
	return t.inner.Close()
}

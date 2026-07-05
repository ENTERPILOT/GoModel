package ratelimit

import (
	"time"

	"gomodel/internal/usage"
)

// UsageTap decorates a usage logger so every recorded entry also feeds token
// rate limit windows. All modalities (non-streaming, SSE streams, realtime,
// audio) funnel through Logger.Write, so one tap covers them all.
type UsageTap struct {
	inner   usage.LoggerInterface
	service *Service
}

// NewUsageTap wraps the logger, or returns it unchanged when there is no
// rate limit service to feed.
func NewUsageTap(inner usage.LoggerInterface, service *Service) usage.LoggerInterface {
	if inner == nil || service == nil {
		return inner
	}
	return &UsageTap{inner: inner, service: service}
}

func (t *UsageTap) Write(entry *usage.UsageEntry) {
	// Cache hits consume no provider tokens and must not count toward token
	// windows, mirroring the budget cost query's cache exclusion.
	if entry != nil && entry.CacheType == "" && entry.TotalTokens > 0 {
		t.service.RecordTokens(entry.UserPath, int64(entry.TotalTokens), time.Now().UTC())
	}
	t.inner.Write(entry)
}

func (t *UsageTap) Config() usage.Config {
	return t.inner.Config()
}

func (t *UsageTap) Close() error {
	return t.inner.Close()
}

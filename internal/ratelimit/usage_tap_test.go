package ratelimit

import (
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/usage"
	"github.com/stretchr/testify/require"
)

type recordingLogger struct {
	entries []*usage.UsageEntry
	closed  bool
}

func (l *recordingLogger) Write(entry *usage.UsageEntry) { l.entries = append(l.entries, entry) }
func (l *recordingLogger) Config() usage.Config          { return usage.Config{Enabled: true} }
func (l *recordingLogger) Close() error                  { l.closed = true; return nil }

func TestUsageTapFeedsTokenWindowsAndDelegates(t *testing.T) {
	service := newTestService(t, Rule{
		Subject:       "/team",
		PeriodSeconds: PeriodMinuteSeconds,
		MaxTokens:     new(int64(1000)),
	})
	inner := &recordingLogger{}
	tap := NewUsageTap(inner, service)

	tap.Write(&usage.UsageEntry{UserPath: "/team/alice", TotalTokens: 40})
	tap.Write(&usage.UsageEntry{UserPath: "/team/alice", TotalTokens: 60, CacheType: "exact"}) // cache hit: skipped
	tap.Write(&usage.UsageEntry{UserPath: "/other", TotalTokens: 500})                         // unmatched path
	tap.Write(nil)

	status := service.Statuses(time.Now().UTC())[0]
	require.Equal(t, int64(40), status.TokensUsed)
	require.Len(t, inner.entries, 4)
	require.True(t, tap.Config().Enabled)
	err := tap.Close()
	require.NoError(t, err)
	require.True(t, inner.closed)
}

func TestUsageTapChargesExecutedProviderAndModel(t *testing.T) {
	service := newTestService(t,
		Rule{Scope: ScopeProvider, Subject: "openai-eu", PeriodSeconds: PeriodMinuteSeconds, MaxTokens: new(int64(1000))},
		Rule{Scope: ScopeModel, Subject: "gpt-4o", PeriodSeconds: PeriodMinuteSeconds, MaxTokens: new(int64(1000))},
	)
	tap := NewUsageTap(&recordingLogger{}, service)

	// Entries record the executed instance name (falls back to type when absent).
	tap.Write(&usage.UsageEntry{UserPath: "/team", Provider: "openai", ProviderName: "openai-eu", Model: "gpt-4o", TotalTokens: 30})
	tap.Write(&usage.UsageEntry{UserPath: "/team", Provider: "openai-eu", Model: "gpt-4o-mini", TotalTokens: 20})

	byScope := map[RuleScope]Status{}
	for _, status := range service.Statuses(time.Now().UTC()) {
		byScope[status.Rule.Scope] = status
	}
	require.Equal(t, int64(50), byScope[ScopeProvider].TokensUsed)
	require.Equal(t, int64(30), byScope[ScopeModel].TokensUsed)
}

func TestNewUsageTapWithoutServiceReturnsInner(t *testing.T) {
	inner := &recordingLogger{}
	got := NewUsageTap(inner, nil)
	require.Equal(t, usage.LoggerInterface(inner), got)
	got = NewUsageTap(nil, nil)
	require.Nil(t, got)
}

package ratelimit

import (
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/usage"
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
	if status.TokensUsed != 40 {
		t.Fatalf("tokens used = %d, want 40 (cache hits and unmatched paths skipped)", status.TokensUsed)
	}
	if len(inner.entries) != 4 {
		t.Fatalf("inner writes = %d, want 4 (tap must always delegate)", len(inner.entries))
	}
	if !tap.Config().Enabled {
		t.Fatal("Config() not delegated")
	}
	if err := tap.Close(); err != nil || !inner.closed {
		t.Fatalf("Close() not delegated: err=%v closed=%v", err, inner.closed)
	}
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
	if byScope[ScopeProvider].TokensUsed != 50 {
		t.Fatalf("provider tokens used = %d, want 50", byScope[ScopeProvider].TokensUsed)
	}
	if byScope[ScopeModel].TokensUsed != 30 {
		t.Fatalf("model tokens used = %d, want 30", byScope[ScopeModel].TokensUsed)
	}
}

func TestNewUsageTapWithoutServiceReturnsInner(t *testing.T) {
	inner := &recordingLogger{}
	if got := NewUsageTap(inner, nil); got != usage.LoggerInterface(inner) {
		t.Fatal("NewUsageTap(inner, nil) should return inner unchanged")
	}
	if got := NewUsageTap(nil, nil); got != nil {
		t.Fatal("NewUsageTap(nil, nil) should return nil")
	}
}

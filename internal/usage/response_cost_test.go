package usage

import (
	"bytes"
	"context"
	"log/slog"
	"maps"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestResponseCost(t *testing.T) {
	tests := []struct {
		name   string
		entry  *UsageEntry
		billed bool
		want   float64
		known  bool
	}{
		{name: "provider cost", entry: &UsageEntry{TotalCost: new(0.25), BilledCost: new(0.5)}, want: 0.25, known: true},
		{name: "a known zero is a cost", entry: &UsageEntry{TotalCost: new(0.0)}, want: 0, known: true},
		{name: "unpriced model", entry: &UsageEntry{}},
		{name: "unpriced model with markups", entry: &UsageEntry{BilledCost: new(0.0)}, billed: true},
		{name: "cache hit costs nothing upstream", entry: &UsageEntry{CacheType: CacheTypeExact, TotalCost: new(0.25)}, want: 0, known: true},
		{name: "unpriced cache hit", entry: &UsageEntry{CacheType: CacheTypeSemantic}},
		{name: "billed cost", entry: &UsageEntry{TotalCost: new(0.25), BilledCost: new(0.5)}, billed: true, want: 0.5, known: true},
		{name: "billed cache hit", entry: &UsageEntry{CacheType: CacheTypeExact, TotalCost: new(0.25), BilledCost: new(0.5)}, billed: true, want: 0.5, known: true},
		{name: "unbilled cache hit", entry: &UsageEntry{CacheType: CacheTypeExact, TotalCost: new(0.25), BilledCost: new(0.0)}, billed: true, want: 0, known: true},
		{name: "unpriced unbilled cache hit", entry: &UsageEntry{CacheType: CacheTypeExact, BilledCost: new(0.0)}, billed: true},
		{name: "non-billable provider", entry: nonBillable(&UsageEntry{TotalCost: new(0.25), BilledCost: new(0.0)}), billed: true, want: 0, known: true},
		{name: "unpriced non-billable provider", entry: nonBillable(&UsageEntry{BilledCost: new(0.0)}), billed: true},
		{name: "non-billable provider without markups", entry: nonBillable(&UsageEntry{TotalCost: new(0.25)}), want: 0.25, known: true},
		{name: "billed never falls back to the provider cost", entry: &UsageEntry{TotalCost: new(0.25)}, billed: true},
		{name: "invalid billed cost", entry: &UsageEntry{TotalCost: new(0.25), BilledCost: new(math.NaN())}, billed: true},
		{name: "not a number", entry: &UsageEntry{TotalCost: new(math.NaN())}},
		{name: "infinite", entry: &UsageEntry{TotalCost: new(math.Inf(1))}},
		{name: "negative", entry: &UsageEntry{TotalCost: new(-1.0)}},
		{name: "no entry"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, known := ResponseCost(tt.entry, tt.billed)

			assert.Equal(t, tt.known, known)
			assert.Equal(t, tt.want, got)
		})
	}
}

func nonBillable(entry *UsageEntry) *UsageEntry {
	entry.MarkNonBillable()
	return entry
}

func TestCostReportKeepsTheLatestRecordedCost(t *testing.T) {
	ctx, report := WithCostReport(context.Background(), false)
	_, known := report.Cost()
	require.False(t, known)

	ReportCost(ctx, nil, &UsageEntry{TotalCost: new(0.25)})
	cost, known := report.Cost()
	require.True(t, known)
	assert.Equal(t, 0.25, cost)

	ReportCost(ctx, nil, nil)
	_, known = report.Cost()
	assert.True(t, known, "a missing entry reports nothing")

	ReportCost(ctx, nil, &UsageEntry{})
	_, known = report.Cost()
	assert.False(t, known, "an unpriced entry makes the cost unknown again")
}

// A forgotten cost is unknown until the next entry is recorded.
func TestCostReportForgetsItsCost(t *testing.T) {
	ctx, report := WithCostReport(context.Background(), false)
	ReportCost(ctx, nil, &UsageEntry{TotalCost: new(0.25)})

	report.Forget()
	cost, known := report.Cost()
	require.False(t, known)
	assert.Zero(t, cost)

	ReportCost(ctx, nil, &UsageEntry{TotalCost: new(0.5)})
	cost, known = report.Cost()
	require.True(t, known)
	assert.Equal(t, 0.5, cost)
}

func TestCostReportIsNilSafe(t *testing.T) {
	var report *CostReport
	report.Forget()
	report.Record(nil, &UsageEntry{TotalCost: new(0.25)})
	_, known := report.Cost()
	assert.False(t, known)

	assert.Nil(t, CostReportFromContext(context.Background()))
	ReportCost(context.Background(), nil, &UsageEntry{TotalCost: new(0.25)})
}

// A model call the gateway makes itself while serving a request (a guardrail,
// a plugin) is not the cost of the client's response.
func TestCostReportIgnoresGatewayInternalCalls(t *testing.T) {
	for _, origin := range []core.RequestOrigin{core.RequestOriginGuardrail, core.RequestOriginPlugin} {
		ctx, report := WithCostReport(context.Background(), false)
		internal := core.WithRequestOrigin(ctx, origin)

		assert.Nil(t, CostReportFromContext(internal))
		ReportCost(internal, nil, &UsageEntry{TotalCost: new(0.25)})

		_, known := report.Cost()
		assert.False(t, known, "origin %s", origin)
	}
}

// stampingLogger stamps a billed cost of twice the provider cost.
type stampingLogger struct {
	recordingTapLogger
}

func (l *stampingLogger) StampBilling(entry *UsageEntry) {
	entry.BilledCost = new(*entry.TotalCost * 2)
}

func TestStampBillingStampsWithoutRecording(t *testing.T) {
	inner := &stampingLogger{}
	tap := NewBillableTap(inner, func(providerName string) bool { return providerName != "byok" })
	billable := &UsageEntry{ProviderName: "openai", TotalCost: new(0.25)}
	own := &UsageEntry{ProviderName: "byok", TotalCost: new(0.25)}

	StampBilling(tap, billable)
	StampBilling(tap, own)
	StampBilling(tap, nil)
	StampBilling(&recordingTapLogger{}, billable)

	assert.True(t, billable.IsBillable())
	assert.False(t, own.IsBillable())
	require.NotNil(t, billable.BilledCost)
	assert.Equal(t, 0.5, *billable.BilledCost)
	assert.Empty(t, inner.entries)
}

func TestCostReportStampsBillingBeforeReadingTheCost(t *testing.T) {
	_, report := WithCostReport(context.Background(), true)

	report.Record(&stampingLogger{}, &UsageEntry{TotalCost: new(0.25)})

	cost, known := report.Cost()
	require.True(t, known)
	assert.Equal(t, 0.5, cost)
}

// A stream reports the cost of the usage seen so far each time an event adds
// to it, so the event carrying the final token counts can carry the final cost.
func TestStreamUsageObserverReportsCostAsUsageArrives(t *testing.T) {
	logger := &trackingLogger{enabled: true}
	pricing := &streamPricingCaptureResolver{pricing: &core.ModelPricing{InputPerMtok: new(1.0), OutputPerMtok: new(2.0)}}
	observer := NewStreamUsageObserver(logger, "claude-fable-5", "anthropic", "req-1", "/v1/messages", pricing)
	_, report := WithCostReport(context.Background(), false)
	observer.SetCostReport(report)

	observer.OnJSONEvent(map[string]any{
		"type":    "message_start",
		"message": map[string]any{"id": "msg_1", "usage": map[string]any{"input_tokens": float64(1_000_000)}},
	})
	cost, known := report.Cost()
	require.True(t, known)
	assert.InDelta(t, 1.0, cost, 1e-9)

	observer.OnJSONEvent(map[string]any{
		"type":  "message_delta",
		"usage": map[string]any{"output_tokens": float64(1_000_000)},
	})
	cost, known = report.Cost()
	require.True(t, known)
	assert.InDelta(t, 3.0, cost, 1e-9)
	require.Empty(t, logger.getEntries(), "usage is recorded once, when the stream closes")

	observer.OnStreamClose()
	entries := logger.getEntries()
	require.Len(t, entries, 1)
	require.NotNil(t, entries[0].TotalCost)
	assert.Equal(t, cost, *entries[0].TotalCost, "the reported cost is the recorded one")
}

// A provider's own cost belongs to the token counts it was reported with. A
// later usage event without one is merged with it for the usage row, but the
// cost of other token counts is not returned as the cost of these.
func TestStreamUsageObserverReturnsNoProviderCostOfOtherTokenCounts(t *testing.T) {
	usageEvent := func(prompt, completion float64, cost map[string]any) map[string]any {
		usage := map[string]any{"prompt_tokens": prompt, "completion_tokens": completion}
		maps.Copy(usage, cost)
		return map[string]any{"usage": usage}
	}
	credits := func(cost float64) map[string]any { return map[string]any{"cost": cost} }
	tests := []struct {
		name      string
		provider  string
		events    []map[string]any // the first one is priced
		wantKnown bool             // of the last one
		wantCost  float64
	}{
		{
			name: "openrouter, other tokens and no cost", provider: "openrouter",
			events: []map[string]any{usageEvent(10, 1, credits(0.001)), usageEvent(20, 1, nil)},
		},
		{
			name: "xai, other tokens and no cost", provider: "xai",
			events: []map[string]any{usageEvent(10, 1, map[string]any{"cost_in_usd_ticks": float64(10_000_000)}), usageEvent(20, 1, nil)},
		},
		{
			name: "edenai, other tokens and no cost", provider: "edenai",
			events: []map[string]any{
				{"cost": 0.001, "usage": map[string]any{"prompt_tokens": float64(10), "completion_tokens": float64(1)}},
				usageEvent(20, 1, nil),
			},
		},
		{
			name: "output tokens reported after the cost", provider: "openrouter",
			events: []map[string]any{
				{"usage": map[string]any{"prompt_tokens": float64(10), "cost": 0.001}},
				{"usage": map[string]any{"completion_tokens": float64(5)}},
			},
		},
		{
			name: "the other tokens again", provider: "openrouter",
			events: []map[string]any{usageEvent(10, 1, credits(0.001)), usageEvent(20, 1, nil), usageEvent(20, 1, nil)},
		},
		{
			name: "other tokens with their own cost", provider: "openrouter",
			events:    []map[string]any{usageEvent(10, 1, credits(0.001)), usageEvent(20, 1, nil), usageEvent(30, 1, credits(0.003))},
			wantKnown: true, wantCost: 0.003,
		},
		{
			name: "the same tokens again, without the cost", provider: "openrouter",
			events:    []map[string]any{usageEvent(10, 1, credits(0.001)), usageEvent(10, 1, nil)},
			wantKnown: true, wantCost: 0.001,
		},
		{
			name: "a provider whose cost is calculated", provider: "openai",
			events:    []map[string]any{usageEvent(1_000_000, 1_000_000, credits(0.001)), usageEvent(2_000_000, 1_000_000, nil)},
			wantKnown: true, wantCost: 4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := &trackingLogger{enabled: true}
			pricing := &streamPricingCaptureResolver{pricing: &core.ModelPricing{InputPerMtok: new(1.0), OutputPerMtok: new(2.0)}}
			observer := NewStreamUsageObserver(logger, "model-1", tt.provider, "req-1", "/v1/chat/completions", pricing)
			_, report := WithCostReport(context.Background(), false)
			observer.SetCostReport(report)

			observer.OnJSONEvent(tt.events[0])
			_, known := report.Cost()
			require.True(t, known, "the first event is priced")
			for _, event := range tt.events[1:] {
				observer.OnJSONEvent(event)
			}

			cost, known := report.Cost()
			assert.Equal(t, tt.wantKnown, known)
			assert.InDelta(t, tt.wantCost, cost, 1e-9)
		})
	}
}

// opaqueWrapper wraps a stamping logger without forwarding StampBilling.
type opaqueWrapper struct {
	recordingTapLogger
	inner LoggerInterface
}

// A logger wrapper that does not forward StampBilling hides the billed cost.
// The response then carries no cost, never the provider cost, and the gap is
// logged as an error instead of passing silently.
func TestCostReportReportsAMissingBillingStamp(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	tests := []struct {
		name    string
		logger  LoggerInterface
		entry   *UsageEntry
		want    float64
		known   bool
		logsErr bool
	}{
		{name: "forwarded", logger: NewBillableTap(&stampingLogger{}, func(string) bool { return true }), entry: &UsageEntry{ProviderName: "openai", TotalCost: new(0.25)}, want: 0.5, known: true},
		{name: "not forwarded", logger: &opaqueWrapper{inner: &stampingLogger{}}, entry: &UsageEntry{RequestID: "req-from-a-header", TotalCost: new(0.25)}, logsErr: true},
		{name: "unpriced is not a missing stamp", logger: &opaqueWrapper{inner: &stampingLogger{}}, entry: &UsageEntry{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs.Reset()
			_, report := WithCostReport(context.Background(), true)

			report.Record(tt.logger, tt.entry)

			cost, known := report.Cost()
			assert.Equal(t, tt.known, known)
			assert.Equal(t, tt.want, cost)
			if tt.logsErr {
				assert.Contains(t, logs.String(), "level=ERROR")
				assert.Contains(t, logs.String(), "did not stamp the billed cost")
				assert.Contains(t, logs.String(), "opaqueWrapper", "the logger that hides the stamp is named")
				assert.NotContains(t, logs.String(), tt.entry.RequestID, "no value of the request is logged")
			} else {
				assert.Empty(t, logs.String())
			}
		})
	}
}

// billingLogger bills an entry the way the markup tap does, when it is stamped
// and again when it is recorded: twice the provider cost, or nothing for a
// provider the caller does not owe.
type billingLogger struct {
	recordingTapLogger
}

func (l *billingLogger) StampBilling(entry *UsageEntry) {
	entry.BilledCost = new(0.0)
	if entry.IsBillable() {
		entry.BilledCost = new(*entry.TotalCost * 2)
	}
}

func (l *billingLogger) Write(entry *UsageEntry) {
	l.StampBilling(entry)
	l.recordingTapLogger.Write(entry)
}

// A provider's billable setting may change between the moment the cost of a
// response is calculated and the moment its usage is recorded. The entry is
// recorded with the decision the returned cost was calculated with, so the
// client is told what it is billed.
func TestBillableTapRecordsTheDecisionTheReturnedCostWasCalculatedWith(t *testing.T) {
	tests := []struct {
		name             string
		stamped, written bool // the provider's billable setting at each moment
		want             float64
	}{
		{name: "billable, then not", stamped: true, written: false, want: 0.5},
		{name: "not billable, then billable", stamped: false, written: true, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			billable, lookups := tt.stamped, 0
			inner := &billingLogger{}
			tap := NewBillableTap(inner, func(string) bool { lookups++; return billable })
			_, report := WithCostReport(context.Background(), true)
			entry := &UsageEntry{ProviderName: "openai", TotalCost: new(0.25)}

			report.Record(tap, entry)
			billable = tt.written
			tap.Write(entry)

			cost, known := report.Cost()
			require.True(t, known)
			assert.Equal(t, tt.want, cost)
			require.Len(t, inner.entries, 1)
			assert.Equal(t, tt.stamped, inner.entries[0].IsBillable())
			require.NotNil(t, inner.entries[0].BilledCost)
			assert.Equal(t, cost, *inner.entries[0].BilledCost, "the recorded billed cost is the returned one")
			assert.Equal(t, 1, lookups)
		})
	}
}

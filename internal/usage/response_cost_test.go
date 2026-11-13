package usage

import (
	"bytes"
	"context"
	"log/slog"
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

func TestCostReportIsNilSafe(t *testing.T) {
	var report *CostReport
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
		{name: "not forwarded", logger: &opaqueWrapper{inner: &stampingLogger{}}, entry: &UsageEntry{TotalCost: new(0.25)}, logsErr: true},
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
			} else {
				assert.Empty(t, logs.String())
			}
		})
	}
}

package budget

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckResultProjection(t *testing.T) {
	start := time.Date(2026, time.April, 25, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	resetAt := start.Add(12 * time.Hour)
	at := func(t time.Time) *time.Time { return &t }

	tests := []struct {
		name           string
		result         CheckResult
		now            time.Time
		wantOK         bool
		wantSpend      float64
		wantExhaustion *time.Time
	}{
		{
			name:   "below the floor",
			result: CheckResult{Budget: Budget{Amount: 100}, PeriodStart: start, PeriodEnd: end, Spent: 5, HasUsage: true},
			now:    start.Add(71 * time.Minute), // 4.9% of a day
		},
		{
			name:           "at the floor",
			result:         CheckResult{Budget: Budget{Amount: 100}, PeriodStart: start, PeriodEnd: end, Spent: 10, HasUsage: true},
			now:            start.Add(72 * time.Minute), // 5% of a day
			wantOK:         true,
			wantSpend:      200,
			wantExhaustion: at(start.Add(12 * time.Hour)),
		},
		{
			name:      "on track to stay within the budget",
			result:    CheckResult{Budget: Budget{Amount: 100}, PeriodStart: start, PeriodEnd: end, Spent: 30, HasUsage: true},
			now:       start.Add(12 * time.Hour),
			wantOK:    true,
			wantSpend: 60,
		},
		{
			name:           "on track to run out",
			result:         CheckResult{Budget: Budget{Amount: 100}, PeriodStart: start, PeriodEnd: end, Spent: 80, HasUsage: true},
			now:            start.Add(12 * time.Hour),
			wantOK:         true,
			wantSpend:      160,
			wantExhaustion: at(start.Add(15 * time.Hour)),
		},
		{
			name:      "ends exactly on the budget",
			result:    CheckResult{Budget: Budget{Amount: 100}, PeriodStart: start, PeriodEnd: end, Spent: 50, HasUsage: true},
			now:       start.Add(12 * time.Hour),
			wantOK:    true,
			wantSpend: 100,
		},
		{
			name:      "already spent",
			result:    CheckResult{Budget: Budget{Amount: 100}, PeriodStart: start, PeriodEnd: end, Spent: 120, HasUsage: true},
			now:       start.Add(12 * time.Hour),
			wantOK:    true,
			wantSpend: 240,
		},
		{
			name:      "no spend",
			result:    CheckResult{Budget: Budget{Amount: 100}, PeriodStart: start, PeriodEnd: end},
			now:       start.Add(12 * time.Hour),
			wantOK:    true,
			wantSpend: 0,
		},
		{
			name:   "just after a reset",
			result: CheckResult{Budget: Budget{Amount: 100}, PeriodStart: resetAt, PeriodEnd: end, Spent: 5, HasUsage: true},
			now:    resetAt.Add(30 * time.Minute), // 4.2% of what is left of the day
		},
		{
			name:           "after a reset the rate counts from the reset",
			result:         CheckResult{Budget: Budget{Amount: 100}, PeriodStart: resetAt, PeriodEnd: end, Spent: 60, HasUsage: true},
			now:            resetAt.Add(6 * time.Hour),
			wantOK:         true,
			wantSpend:      120,
			wantExhaustion: at(resetAt.Add(10 * time.Hour)),
		},
		{
			name:      "period over",
			result:    CheckResult{Budget: Budget{Amount: 100}, PeriodStart: start, PeriodEnd: end, Spent: 40, HasUsage: true},
			now:       end.Add(time.Hour),
			wantOK:    true,
			wantSpend: 40,
		},
		{
			name: "per-child template has no single spend",
			result: CheckResult{
				Budget:      Budget{Scope: ScopeUserPath, Subject: "/customers", PerChild: true, Amount: 100},
				PeriodStart: start, PeriodEnd: end,
			},
			now: start.Add(12 * time.Hour),
		},
		{
			name: "resolved per-child budget",
			result: CheckResult{
				Budget:      Budget{Scope: ScopeUserPath, Subject: "/customers", PerChild: true, EffectiveSubject: "/customers/alice", Amount: 100},
				PeriodStart: start, PeriodEnd: end, Spent: 25, HasUsage: true,
			},
			now:       start.Add(12 * time.Hour),
			wantOK:    true,
			wantSpend: 50,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.result.Projection(tt.now)
			require.Equal(t, tt.wantOK, ok)
			assert.InDelta(t, tt.wantSpend, got.Spend, 1e-9)
			if tt.wantExhaustion == nil {
				assert.Nil(t, got.ExhaustionAt)
				return
			}
			require.NotNil(t, got.ExhaustionAt)
			assert.WithinDuration(t, *tt.wantExhaustion, *got.ExhaustionAt, time.Millisecond)
		})
	}
}

func TestThresholdReached(t *testing.T) {
	tests := []struct {
		name     string
		spent    float64
		hasUsage bool
		percent  int
		want     bool
	}{
		{name: "below", spent: 7.99, hasUsage: true, percent: 80},
		{name: "exactly at the threshold", spent: 8, hasUsage: true, percent: 80, want: true},
		{name: "above", spent: 9, hasUsage: true, percent: 80, want: true},
		{name: "spent the budget", spent: 10, hasUsage: true, percent: 100, want: true},
		{name: "no priced usage never alerts", spent: 0, percent: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CheckResult{Budget: Budget{Amount: 10}, Spent: tt.spent, HasUsage: tt.hasUsage}
			assert.Equal(t, tt.want, result.thresholdReached(tt.percent))
		})
	}
}

func TestAlertThresholdsEncoding(t *testing.T) {
	tests := []struct {
		name       string
		thresholds []int
		encoded    string
	}{
		{name: "none", encoded: ""},
		{name: "one", thresholds: []int{80}, encoded: "80"},
		{name: "several", thresholds: []int{50, 80, 100}, encoded: "50,80,100"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.encoded, encodeAlertThresholds(tt.thresholds))
			assert.Equal(t, tt.thresholds, decodeAlertThresholds(tt.encoded))
		})
	}
	assert.Equal(t, []int{80}, decodeAlertThresholds("x, 80,,"), "unreadable entries are skipped")
}

func TestServiceAlertThresholdsPreferTheBudgetsOwn(t *testing.T) {
	service := &Service{alertDefaults: []int{80, 100}}
	WithAlertThresholds([]int{60, 90})(service)
	WithAlertThresholds(nil)(service) // empty keeps what is set

	assert.Equal(t, []int{60, 90}, service.alertThresholds(Budget{}))
	assert.Equal(t, []int{25}, service.alertThresholds(Budget{AlertThresholds: []int{25}}))
	assert.Equal(t, AlertSettings{DefaultThresholds: []int{60, 90}}, service.AlertSettings())
}

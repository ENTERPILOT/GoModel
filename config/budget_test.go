package config

import (
	"strings"
	"testing"
)

// The JSON-array form of SET_BUDGET_* decodes through json tags. Without them
// period_seconds was silently dropped, yielding a limit with no window.
func TestParseBudgetEnvLimits_JSONArray(t *testing.T) {
	limits, err := parseBudgetEnvLimits(`[{"period_seconds":7200,"amount":5}]`, true)
	if err != nil {
		t.Fatalf("parseBudgetEnvLimits() error = %v", err)
	}
	if len(limits) != 1 {
		t.Fatalf("len(limits) = %d, want 1", len(limits))
	}
	if limits[0].PeriodSeconds != 7200 {
		t.Fatalf("PeriodSeconds = %d, want 7200", limits[0].PeriodSeconds)
	}
	if limits[0].Amount != 5 {
		t.Fatalf("Amount = %v, want 5", limits[0].Amount)
	}
}

func TestParseBudgetEnvLimits_RejectsUnknownField(t *testing.T) {
	_, err := parseBudgetEnvLimits(`[{"period":"daily","ammount":5}]`, true)
	if err == nil {
		t.Fatal("parseBudgetEnvLimits() error = nil, want unknown-field error")
	}
	if !strings.Contains(err.Error(), "ammount") {
		t.Fatalf("parseBudgetEnvLimits() error = %q, want it to name the unknown field", err)
	}
}

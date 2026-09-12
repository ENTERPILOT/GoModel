package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The JSON-array form of SET_BUDGET_* decodes through json tags. Without them
// period_seconds was silently dropped, yielding a limit with no window.
func TestParseBudgetEnvLimits_JSONArray(t *testing.T) {
	limits, err := parseBudgetEnvLimits(`[{"period_seconds":7200,"amount":5}]`, true)
	require.NoError(t, err)
	require.Len(t, limits, 1)
	require.Equal(t, int64(7200), limits[0].PeriodSeconds)
	require.Equal(t, float64(5), limits[0].Amount)
}

func TestParseBudgetEnvLimits_RejectsUnknownField(t *testing.T) {
	_, err := parseBudgetEnvLimits(`[{"period":"daily","ammount":5}]`, true)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ammount")
}

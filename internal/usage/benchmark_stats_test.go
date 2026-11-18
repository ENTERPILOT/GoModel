package usage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputeCostStats(t *testing.T) {
	tests := []struct {
		name  string
		costs []float64
		want  costStats
	}{
		{name: "one user is every statistic", costs: []float64{7}, want: costStats{Mean: 7, P50: 7, P90: 7, Max: 7}},
		{name: "two users: the median is their mean", costs: []float64{20, 10}, want: costStats{Mean: 15, P50: 15, P90: 19, Max: 20}},
		{name: "odd count", costs: []float64{50, 15, 40, 20, 35}, want: costStats{Mean: 32, P50: 35, P90: 46, Max: 50}},
		{name: "one to ten", costs: []float64{10, 9, 8, 7, 6, 5, 4, 3, 2, 1}, want: costStats{Mean: 5.5, P50: 5.5, P90: 9.1, Max: 10}},
		{name: "zero-cost users count", costs: []float64{0, 0, 0, 12}, want: costStats{Mean: 3, P50: 0, P90: 8.4, Max: 12}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := computeCostStats(tt.costs)
			require.True(t, ok)
			assert.InDelta(t, tt.want.Mean, got.Mean, 1e-9)
			assert.InDelta(t, tt.want.P50, got.P50, 1e-9)
			assert.InDelta(t, tt.want.P90, got.P90, 1e-9)
			assert.InDelta(t, tt.want.Max, got.Max, 1e-9)
		})
	}

	t.Run("no users", func(t *testing.T) {
		_, ok := computeCostStats(nil)
		assert.False(t, ok)
	})
}

func TestChangePercent(t *testing.T) {
	tests := []struct {
		name              string
		previous, current *float64
		want              *float64
	}{
		{name: "growth", previous: new(10.0), current: new(12.5), want: new(25.0)},
		{name: "drop to zero", previous: new(4.0), current: new(0.0), want: new(-100.0)},
		{name: "no previous users", current: new(3.0)},
		{name: "no current users", previous: new(3.0)},
		{name: "previous zero", previous: new(0.0), current: new(3.0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, changePercent(tt.previous, tt.current))
		})
	}
}

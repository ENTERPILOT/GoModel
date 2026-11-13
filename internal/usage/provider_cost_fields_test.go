package usage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// streamEventWith is a stream event with one usage object and field reported
// where a provider reports it.
func streamEventWith(field ProviderCostField, value any) map[string]any {
	usage := map[string]any{"prompt_tokens": float64(10), "completion_tokens": float64(5), "total_tokens": float64(15)}
	event := map[string]any{"id": "chatcmpl-1", "model": "m", "usage": usage}
	if field.Root {
		event[field.Name] = value
	} else {
		usage[field.Name] = value
	}
	return event
}

// The table lists what the readers read: every field of it reaches the raw
// usage data, from where CalculateUsageCost takes a provider's cost, and a
// member that is not in it is not read as a cost.
func TestProviderCostFields_AreWhatTheReaderReads(t *testing.T) {
	fields := ProviderCostFields()
	require.NotEmpty(t, fields)
	for _, field := range fields {
		var value any = 0.25
		if field.Object {
			value = map[string]any{"upstream_inference_cost": 0.25}
		}
		observer := NewStreamUsageObserver(&trackingLogger{enabled: true}, "m", "openrouter", "req", "/v1/chat/completions", nil)

		entry := observer.extractUsageFromEvent(streamEventWith(field, value))

		require.NotNil(t, entry, field.Name)
		assert.Equal(t, value, entry.RawData[field.Name], "%+v", field)
	}

	observer := NewStreamUsageObserver(&trackingLogger{enabled: true}, "m", "openrouter", "req", "/v1/chat/completions", nil)
	entry := observer.extractUsageFromEvent(streamEventWith(ProviderCostField{Name: "estimated_cost"}, 0.25))
	require.NotNil(t, entry)
	assert.NotContains(t, entry.RawData, "estimated_cost", "a member the table does not list is not read as a cost")
}

// Every provider-reported cost CalculateUsageCost prices from is a field of
// the table, so a response that must not reveal the provider cost loses it.
func TestProviderCostFields_CoverEveryProviderReportedCost(t *testing.T) {
	tests := []struct {
		provider string
		rawData  map[string]any
		source   string
	}{
		{provider: "openrouter", rawData: map[string]any{providerCostMember: 0.5}, source: CostSourceOpenRouterCredits},
		{provider: "xai", rawData: map[string]any{providerCostTicksMember: 5e9}, source: CostSourceXAITicks},
		{provider: "edenai", rawData: map[string]any{providerCostMember: 0.5}, source: CostSourceEdenAICost},
	}
	for _, tt := range tests {
		result := CalculateUsageCost(10, 5, tt.rawData, tt.provider, nil)

		require.Equal(t, tt.source, result.Source)
		require.True(t, isProviderReportedCostSource(result.Source))
		for name := range tt.rawData {
			assert.True(t, IsProviderCostField(name, false), name)
		}
	}
}

func TestIsProviderCostField(t *testing.T) {
	tests := []struct {
		name string
		root bool
		want bool
	}{
		{name: "cost", want: true},
		{name: "COST", want: true},
		{name: "coſt", want: true},
		{name: "cost_in_usd_ticks", want: true},
		{name: "Cost_Details", want: true},
		{name: "cost", root: true, want: true},
		{name: "cost_details", root: true},
		{name: "cost_in_usd_ticks", root: true},
		{name: "is_byok"},
		{name: "estimated_cost"},
		{name: ""},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, IsProviderCostField(tt.name, tt.root), "%q root=%v", tt.name, tt.root)
	}
}

// A usage-level cost wins over a root-level one, and an unusable value is
// not read at all.
func TestCopyProviderCosts(t *testing.T) {
	tests := []struct {
		name  string
		usage map[string]any
		root  map[string]any
		want  map[string]any
	}{
		{name: "root cost", root: map[string]any{"cost": 0.5}, want: map[string]any{"cost": 0.5}},
		{name: "usage cost wins", usage: map[string]any{"cost": 0.25}, root: map[string]any{"cost": 0.5}, want: map[string]any{"cost": 0.25}},
		{name: "negative", usage: map[string]any{"cost": -1.0, "cost_in_usd_ticks": -1.0}, want: map[string]any{}},
		{name: "not a number", usage: map[string]any{"cost": map[string]any{"total": 1.0}}, root: map[string]any{"cost": true}, want: map[string]any{}},
		{name: "details that are no object", usage: map[string]any{"cost_details": 0.5}, want: map[string]any{}},
		{name: "root members of a usage object's kind", root: map[string]any{"cost_details": map[string]any{"a": 1.0}, "cost_in_usd_ticks": 5.0}, want: map[string]any{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rawData := map[string]any{}

			copyProviderCosts(rawData, tt.usage, false)
			copyProviderCosts(rawData, tt.root, true)

			assert.Equal(t, tt.want, rawData)
		})
	}
}

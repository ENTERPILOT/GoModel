package usage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestExtractFromModerationResponse(t *testing.T) {
	resp := &core.ModerationResponse{ID: "modr-1", Model: "omni-moderation-2024-09-26", Results: []core.ModerationResult{{}, {}}}
	zero, rate := 0.0, 0.1

	tests := []struct {
		name       string
		pricing    *core.ModelPricing
		wantCaveat bool
	}{
		{name: "no pricing", pricing: nil},
		{name: "free model", pricing: &core.ModelPricing{InputPerMtok: &zero}},
		{name: "token priced model", pricing: &core.ModelPricing{InputPerMtok: &rate}, wantCaveat: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := ExtractFromModerationResponse(resp, "req-1", "omni-moderation-latest", "openai", tt.pricing)
			require.NotNil(t, entry)
			assert.Equal(t, "req-1", entry.RequestID)
			assert.Equal(t, "omni-moderation-latest", entry.Model)
			assert.Equal(t, "openai", entry.Provider)
			assert.Equal(t, "/v1/moderations", entry.Endpoint)
			assert.Zero(t, entry.TotalTokens)
			assert.Equal(t, 2, entry.RawData[rawKeyModerationInputs])
			if tt.wantCaveat {
				assert.Equal(t, caveatEmbeddingMissingUsage, entry.CostsCalculationCaveat)
			} else {
				assert.Empty(t, entry.CostsCalculationCaveat)
			}
		})
	}

	assert.Nil(t, ExtractFromModerationResponse(nil, "req-1", "m", "openai"))
}

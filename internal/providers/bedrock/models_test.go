package bedrock

import (
	"testing"

	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrock/types"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFoundationModelMetadata(t *testing.T) {
	streaming := true
	noStreaming := false
	tests := []struct {
		name    string
		summary bedrocktypes.FoundationModelSummary
		want    *core.ModelMetadata
	}{
		{
			name: "vision model that streams",
			summary: bedrocktypes.FoundationModelSummary{
				ModelName:                  new(" Claude Sonnet 4.5 "),
				InputModalities:            []bedrocktypes.ModelModality{bedrocktypes.ModelModalityText, bedrocktypes.ModelModalityImage},
				OutputModalities:           []bedrocktypes.ModelModality{bedrocktypes.ModelModalityText},
				ResponseStreamingSupported: &streaming,
			},
			want: &core.ModelMetadata{
				DisplayName:  "Claude Sonnet 4.5",
				Modes:        []string{"chat"},
				Categories:   []core.ModelCategory{core.CategoryTextGeneration},
				Capabilities: map[string]bool{"vision": true, "streaming": true},
			},
		},
		{
			name: "text-only model without streaming",
			summary: bedrocktypes.FoundationModelSummary{
				InputModalities:            []bedrocktypes.ModelModality{bedrocktypes.ModelModalityText},
				ResponseStreamingSupported: &noStreaming,
			},
			want: &core.ModelMetadata{
				Modes:        []string{"chat"},
				Categories:   []core.ModelCategory{core.CategoryTextGeneration},
				Capabilities: map[string]bool{"streaming": false},
			},
		},
		{
			name:    "summary that reports nothing",
			summary: bedrocktypes.FoundationModelSummary{},
			want: &core.ModelMetadata{
				Modes:      []string{"chat"},
				Categories: []core.ModelCategory{core.CategoryTextGeneration},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := foundationModelMetadata(tt.summary)
			require.NotNil(t, got)
			assert.Equal(t, tt.want, got)
		})
	}
}

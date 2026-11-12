package core

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImageUsage_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		body string
		want ImageUsage
	}{
		{
			name: "openai images naming",
			body: `{"input_tokens":50,"output_tokens":1000,"total_tokens":1050,"input_tokens_details":{"text_tokens":10,"image_tokens":40},"output_tokens_details":{"image_tokens":1000}}`,
			want: ImageUsage{
				InputTokens: 50, OutputTokens: 1000, TotalTokens: 1050,
				InputTokensDetails:  &ImageTokenDetails{TextTokens: 10, ImageTokens: 40},
				OutputTokensDetails: &ImageTokenDetails{ImageTokens: 1000},
			},
		},
		{
			name: "openrouter chat naming with cost",
			body: `{"prompt_tokens":12,"completion_tokens":664,"total_tokens":676,"completion_tokens_details":{"image_tokens":664},"cost":0.01998,"is_byok":false,"cost_details":{"upstream_inference_completions_cost":0.01992}}`,
			want: ImageUsage{
				InputTokens: 12, OutputTokens: 664, TotalTokens: 676,
				OutputTokensDetails: &ImageTokenDetails{ImageTokens: 664},
				RawUsage: map[string]any{
					"cost":         0.01998,
					"is_byok":      false,
					"cost_details": map[string]any{"upstream_inference_completions_cost": 0.01992},
				},
			},
		},
		{
			name: "images naming wins over chat naming",
			body: `{"input_tokens":5,"prompt_tokens":7,"output_tokens":9,"completion_tokens":11}`,
			want: ImageUsage{InputTokens: 5, OutputTokens: 9},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got ImageUsage
			require.NoError(t, json.Unmarshal([]byte(tt.body), &got))
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestImageUsage_MarshalJSON(t *testing.T) {
	t.Run("without extras keeps the images shape", func(t *testing.T) {
		body, err := json.Marshal(ImageUsage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3})
		require.NoError(t, err)
		assert.JSONEq(t, `{"input_tokens":1,"output_tokens":2,"total_tokens":3}`, string(body))
	})

	t.Run("passes provider extras through", func(t *testing.T) {
		var usage ImageUsage
		require.NoError(t, json.Unmarshal([]byte(`{"prompt_tokens":12,"completion_tokens":664,"total_tokens":676,"completion_tokens_details":{"image_tokens":664},"cost":0.01998}`), &usage))

		body, err := json.Marshal(usage)
		require.NoError(t, err)
		assert.JSONEq(t, `{"input_tokens":12,"output_tokens":664,"total_tokens":676,"output_tokens_details":{"text_tokens":0,"image_tokens":664},"cost":0.01998}`, string(body))
	})
}

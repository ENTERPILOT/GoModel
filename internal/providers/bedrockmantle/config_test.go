package bedrockmantle

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveEndpoint(t *testing.T) {
	tests := []struct {
		name       string
		baseURL    string
		apiMode    string
		wantURL    string
		wantRegion string
		wantMode   string
	}{
		{
			name:       "region",
			baseURL:    "us-west-2",
			wantURL:    "https://bedrock-mantle.us-west-2.api.aws",
			wantRegion: "us-west-2",
			wantMode:   modeAuto,
		},
		{
			name:       "full model endpoint",
			baseURL:    "https://bedrock-mantle.us-east-2.api.aws/openai/v1/responses",
			apiMode:    "OPENAI",
			wantURL:    "https://bedrock-mantle.us-east-2.api.aws",
			wantRegion: "us-east-2",
			wantMode:   modeOpenAI,
		},
		{
			name:       "custom proxy prefix",
			baseURL:    "https://proxy.example/bedrock/openai/v1/",
			apiMode:    modeStandard,
			wantURL:    "https://proxy.example/bedrock",
			wantRegion: defaultRegion,
			wantMode:   modeStandard,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BEDROCK_MANTLE_REGION", "")
			t.Setenv("AWS_REGION", "")
			t.Setenv("AWS_DEFAULT_REGION", "")
			got, err := resolveEndpoint(tt.baseURL, tt.apiMode)
			require.NoError(t, err)
			assert.Equal(t, tt.wantURL, got.baseURL)
			assert.Equal(t, tt.wantRegion, got.region)
			assert.Equal(t, tt.wantMode, got.mode)
		})
	}
}

func TestResolveEndpointRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		baseURL string
		apiMode string
	}{
		{baseURL: "not a region"},
		{baseURL: "ftp://example.com"},
		{baseURL: "us-east-1", apiMode: "legacy"},
	}
	for _, tt := range tests {
		_, err := resolveEndpoint(tt.baseURL, tt.apiMode)
		assert.Error(t, err, "resolveEndpoint(%q, %q)", tt.baseURL, tt.apiMode)
	}
}

func TestResolveEndpointRejectsInvalidEnvironmentRegion(t *testing.T) {
	t.Setenv("BEDROCK_MANTLE_REGION", "not-a-region")
	_, err := resolveEndpoint("", "")
	require.Error(t, err)
}

func TestUsesOpenAIPath(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{model: "openai.gpt-5.6-sol", want: true},
		{model: "openai.gpt-5.6-terra", want: true},
		{model: "openai.gpt-5.6-luna", want: true},
		{model: "google.gemma-4-27b-it", want: true},
		{model: "xai.grok-4.3-fast", want: true},
		{model: "openai.gpt-oss-120b", want: false},
		{model: "amazon.nova-2-lite-v1:0", want: false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, usesOpenAIPath(tt.model), "usesOpenAIPath(%q)", tt.model)
	}
}

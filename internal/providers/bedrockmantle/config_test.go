package bedrockmantle

import "testing"

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
			if err != nil {
				t.Fatalf("resolveEndpoint() error = %v", err)
			}
			if got.baseURL != tt.wantURL || got.region != tt.wantRegion || got.mode != tt.wantMode {
				t.Errorf("resolveEndpoint() = %+v, want URL %q, region %q, mode %q", got, tt.wantURL, tt.wantRegion, tt.wantMode)
			}
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
		if _, err := resolveEndpoint(tt.baseURL, tt.apiMode); err == nil {
			t.Errorf("resolveEndpoint(%q, %q) error = nil", tt.baseURL, tt.apiMode)
		}
	}
}

func TestResolveEndpointRejectsInvalidEnvironmentRegion(t *testing.T) {
	t.Setenv("BEDROCK_MANTLE_REGION", "not-a-region")
	if _, err := resolveEndpoint("", ""); err == nil {
		t.Fatal("resolveEndpoint() error = nil")
	}
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
		if got := usesOpenAIPath(tt.model); got != tt.want {
			t.Errorf("usesOpenAIPath(%q) = %v, want %v", tt.model, got, tt.want)
		}
	}
}

package core

import "testing"

func TestRequestModelResolutionRequestedQualifiedModel(t *testing.T) {
	tests := []struct {
		name string
		in   *RequestModelResolution
		want string
	}{
		{
			name: "raw alias with slash and no explicit provider stays raw",
			in: &RequestModelResolution{
				Requested: NewRequestedModelSelector("anthropic/claude-opus-4-6", ""),
			},
			want: "anthropic/claude-opus-4-6",
		},
		{
			name: "explicit provider with provider-prefixed model normalizes once",
			in: &RequestModelResolution{
				Requested: NewRequestedModelSelector("openai/gpt-4o", "openai"),
			},
			want: "openai/gpt-4o",
		},
		{
			name: "explicit provider without prefix becomes qualified model",
			in: &RequestModelResolution{
				Requested: NewRequestedModelSelector("gpt-4o", "openai"),
			},
			want: "openai/gpt-4o",
		},
		{
			name: "explicit provider preserves raw slash model",
			in: &RequestModelResolution{
				Requested: NewRequestedModelSelector("openai/gpt-oss-120b", "groq"),
			},
			want: "groq/openai/gpt-oss-120b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.RequestedQualifiedModel(); got != tt.want {
				t.Fatalf("RequestedQualifiedModel() = %q, want %q", got, tt.want)
			}
		})
	}
}

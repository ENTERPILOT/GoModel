package anthropicapi

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
)

func TestWithinCeilings(t *testing.T) {
	enabled := func(budget int) *Thinking { return &Thinking{Type: "enabled", BudgetTokens: budget} }
	effort := func(level string) *OutputConfig { return &OutputConfig{Effort: level} }
	tests := []struct {
		name    string
		req     MessagesRequest
		effort  string
		tokens  int
		want    MessagesRequest
		lowered bool
	}{
		{name: "no ceilings", req: MessagesRequest{MaxTokens: 64000, Thinking: enabled(63000), OutputConfig: effort("max")}},
		{name: "within both", req: MessagesRequest{MaxTokens: 1000, Thinking: enabled(2000), OutputConfig: effort("low")}, effort: "low", tokens: 1000},
		{name: "no thinking and no effort adds none", req: MessagesRequest{MaxTokens: 1000}, effort: "none", tokens: 1000},
		{
			name: "max_tokens above the cap", req: MessagesRequest{MaxTokens: 64000}, tokens: 1000,
			want: MessagesRequest{MaxTokens: 1000}, lowered: true,
		},
		{
			name: "a budget above the ceiling level's own", req: MessagesRequest{MaxTokens: 64000, Thinking: enabled(30000)}, effort: "medium",
			want: MessagesRequest{MaxTokens: 64000, Thinking: enabled(10000)}, lowered: true,
		},
		{
			name: "a budget shrinks to fit under the capped max_tokens", req: MessagesRequest{MaxTokens: 64000, Thinking: enabled(30000)}, effort: "high", tokens: 8000,
			want: MessagesRequest{MaxTokens: 8000, Thinking: enabled(8000 - 1024)}, lowered: true,
		},
		{
			name: "thinking is turned off when no budget fits", req: MessagesRequest{MaxTokens: 64000, Thinking: enabled(2000)}, tokens: 1024,
			want: MessagesRequest{MaxTokens: 1024, Thinking: &Thinking{Type: "disabled"}}, lowered: true,
		},
		{name: "the client's own max_tokens is not fitted", req: MessagesRequest{MaxTokens: 900, Thinking: enabled(2000)}, tokens: 1000},
		{
			name: "a none ceiling turns thinking off", req: MessagesRequest{MaxTokens: 2048, Thinking: &Thinking{Type: "adaptive"}}, effort: "none",
			want: MessagesRequest{MaxTokens: 2048, Thinking: &Thinking{Type: "disabled"}}, lowered: true,
		},
		{
			name: "an effort above the ceiling keeps the format",
			req:  MessagesRequest{MaxTokens: 2048, OutputConfig: &OutputConfig{Effort: "max", Format: json.RawMessage(`{"type":"json_schema"}`)}}, effort: "medium",
			want: MessagesRequest{MaxTokens: 2048, OutputConfig: &OutputConfig{Effort: "medium", Format: json.RawMessage(`{"type":"json_schema"}`)}}, lowered: true,
		},
		{
			name: "an effort outside the scale cannot pass", req: MessagesRequest{MaxTokens: 2048, OutputConfig: effort("ultra")}, effort: "high",
			want: MessagesRequest{MaxTokens: 2048, OutputConfig: effort("high")}, lowered: true,
		},
		{
			name: "a level below Anthropic's scale is its lowest", req: MessagesRequest{MaxTokens: 2048, OutputConfig: effort("high")}, effort: "minimal",
			want: MessagesRequest{MaxTokens: 2048, OutputConfig: effort("low")}, lowered: true,
		},
		{
			name: "adaptive thinking without an effort gets the ceiling", req: MessagesRequest{MaxTokens: 2048, Thinking: &Thinking{Type: "adaptive"}}, effort: "low",
			want: MessagesRequest{MaxTokens: 2048, Thinking: &Thinking{Type: "adaptive"}, OutputConfig: effort("low")}, lowered: true,
		},
		{name: "adaptive thinking within the ceiling", req: MessagesRequest{MaxTokens: 2048, Thinking: &Thinking{Type: "adaptive"}}, effort: "medium"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sent := tt.req
			got, lowered := WithinCeilings(&tt.req, tt.effort, tt.tokens)

			assert.Equal(t, tt.lowered, lowered)
			assert.Equal(t, sent, tt.req, "the request must not be mutated")
			if !tt.lowered {
				assert.Same(t, &tt.req, got)
				return
			}
			assert.Equal(t, tt.want, *got)
			_, again := WithinCeilings(got, tt.effort, tt.tokens)
			assert.False(t, again, "a lowered request is within the ceilings")
		})
	}
}

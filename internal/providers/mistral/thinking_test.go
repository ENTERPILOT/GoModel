package mistral

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
)

func TestFlattenThinkingChunks(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		want        string
		wantChanged bool
	}{
		{
			name:        "message content array flattens to string plus reasoning_content",
			in:          `{"choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"hmm"}]},{"type":"text","text":"42"}]}}]}`,
			want:        `{"choices":[{"index":0,"message":{"content":"42","reasoning_content":"hmm","role":"assistant"}}]}`,
			wantChanged: true,
		},
		{
			name:        "delta content array in a stream chunk",
			in:          `{"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}]}}]}`,
			want:        `{"choices":[{"index":0,"delta":{"content":"","reasoning_content":"ab"}}],"object":"chat.completion.chunk"}`,
			wantChanged: true,
		},
		{
			name:        "transition chunk with thinking and text in one delta",
			in:          `{"choices":[{"index":0,"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"t"}],"signature":"sig"},{"type":"text","text":"answer"}]}}]}`,
			want:        `{"choices":[{"index":0,"delta":{"content":"answer","reasoning_content":"t"}}]}`,
			wantChanged: true,
		},
		{
			name:        "string content is untouched",
			in:          `{"choices":[{"index":0,"delta":{"content":"hi"}}]}`,
			want:        `{"choices":[{"index":0,"delta":{"content":"hi"}}]}`,
			wantChanged: false,
		},
		{
			name:        "null content is untouched",
			in:          `{"choices":[{"index":0,"message":{"role":"assistant","content":null}}]}`,
			want:        `{"choices":[{"index":0,"message":{"role":"assistant","content":null}}]}`,
			wantChanged: false,
		},
		{
			name:        "unknown chunk types are grouped in the vendor member, content stays a string",
			in:          `{"choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"text","text":"see "},{"type":"reference","reference_ids":[7]}]}}]}`,
			want:        `{"choices":[{"index":0,"message":{"role":"assistant","content":"see ","mistral_content_chunks":[{"type":"reference","reference_ids":[7]}]}}]}`,
			wantChanged: true,
		},
		{
			name:        "non-chat payloads are untouched",
			in:          `{"object":"list","data":[{"id":"mistral-large-latest"}]}`,
			want:        `{"object":"list","data":[{"id":"mistral-large-latest"}]}`,
			wantChanged: false,
		},
		{
			name:        "not JSON",
			in:          `[DONE]`,
			want:        `[DONE]`,
			wantChanged: false,
		},
		{
			name:        "thinking as a plain string is tolerated",
			in:          `{"choices":[{"index":0,"message":{"content":[{"type":"thinking","thinking":"raw"},{"type":"text","text":"ok"}]}}]}`,
			want:        `{"choices":[{"index":0,"message":{"content":"ok","reasoning_content":"raw"}}]}`,
			wantChanged: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := flattenThinkingChunks([]byte(tt.in))
			assert.Equal(t, tt.wantChanged, changed)
			if json.Valid([]byte(tt.want)) {
				assert.JSONEq(t, tt.want, string(got))
			} else {
				assert.Equal(t, tt.want, string(got))
			}
		})
	}
}

func TestFlattenThinkingChunks_PreservesOtherMembers(t *testing.T) {
	in := `{"id":"cmpl-1","object":"chat.completion.chunk","model":"mistral-large-4-0","created":1773842038,"choices":[{"index":0,"delta":{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"x"}]}]},"finish_reason":null}],"usage":null}`
	got, changed := flattenThinkingChunks([]byte(in))
	assert.True(t, changed)
	s := string(got)
	assert.Contains(t, s, `"id":"cmpl-1"`)
	assert.Contains(t, s, `"model":"mistral-large-4-0"`)
	assert.Contains(t, s, `"created":1773842038`)
	assert.Contains(t, s, `"role":"assistant"`)
	assert.Contains(t, s, `"reasoning_content":"x"`)
}

func TestHasContentArray(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"compact array", `{"delta":{"content":[{"type":"text"}]}}`, true},
		{"spaced array (Python-style separators)", `{"delta": {"content" : [ {"type": "text"}]}}`, true},
		{"string content", `{"delta":{"content":"hi"}}`, false},
		{"null content", `{"message":{"content":null}}`, false},
		{"string then array", `{"a":{"content":"x"},"b":{"content":[1]}}`, true},
		{"escaped key inside a string value", `{"delta":{"content":"say \"content\": [1]"}}`, false},
		{"no content member", `{"choices":[{"finish_reason":"stop"}],"usage":{}}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, hasContentArray([]byte(tt.in)))
		})
	}
}

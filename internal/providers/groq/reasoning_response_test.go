package groq

import (
	"io"
	"strings"
	"testing"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestNormalizeChatResponse(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    string
		absent  string
	}{
		{
			name:    "groq reasoning is renamed",
			message: `{"role":"assistant","content":"4","reasoning":"Answer 4."}`,
			want:    `"reasoning_content":"Answer 4."`,
			absent:  `"reasoning":`,
		},
		{
			name:    "existing reasoning_content wins and the duplicate goes",
			message: `{"role":"assistant","content":"4","reasoning":"drop","reasoning_content":"keep"}`,
			want:    `"reasoning_content":"keep"`,
			absent:  `"reasoning":`,
		},
		{
			name:    "other members are untouched",
			message: `{"role":"assistant","content":"4","channel":"final"}`,
			want:    `"channel":"final"`,
			absent:  `"reasoning_content"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var msg core.ResponseMessage
			if err := json.Unmarshal([]byte(tt.message), &msg); err != nil {
				t.Fatalf("unmarshal message: %v", err)
			}
			resp := &core.ChatResponse{Choices: []core.Choice{{Message: msg}}}

			normalizeChatResponse(resp)

			encoded, err := json.Marshal(resp.Choices[0].Message)
			if err != nil {
				t.Fatalf("marshal message: %v", err)
			}
			if !strings.Contains(string(encoded), tt.want) {
				t.Errorf("message = %s, want it to contain %s", encoded, tt.want)
			}
			if tt.absent != "" && strings.Contains(string(encoded), tt.absent) {
				t.Errorf("message = %s, want it to drop %s", encoded, tt.absent)
			}
		})
	}
}

func TestNormalizeChatResponseNilIsSafe(t *testing.T) {
	normalizeChatResponse(nil)
}

func TestNormalizeChatStream(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "reasoning delta is renamed",
			in:   "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning\":\"We\"}}]}\n\n",
			want: "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"We\"},\"index\":0}]}\n\n",
		},
		{
			name: "content deltas are relayed byte for byte",
			in:   "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n",
			want: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n",
		},
		{
			name: "reasoning_content is left alone",
			in:   "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"We\"}}]}\n\n",
			want: "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"We\"}}]}\n\n",
		},
		{
			name: "the word reasoning in content is not rewritten",
			in:   "data: {\"choices\":[{\"delta\":{\"content\":\"the \\\"reasoning\\\" step\"}}]}\n\n",
			want: "data: {\"choices\":[{\"delta\":{\"content\":\"the \\\"reasoning\\\" step\"}}]}\n\n",
		},
		{
			name: "duplicate spellings collapse onto the canonical one",
			in:   "data: {\"choices\":[{\"delta\":{\"reasoning\":\"drop\",\"reasoning_content\":\"keep\"}}]}\n\n",
			want: "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"keep\"}}]}\n\n",
		},
		{
			// A map[string]any round trip would round this to ...992.
			name: "unrelated numbers keep full precision",
			in:   "data: {\"vendor_counter\":9007199254740993,\"choices\":[{\"delta\":{\"reasoning\":\"We\"}}]}\n\n",
			want: "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"We\"}}],\"vendor_counter\":9007199254740993}\n\n",
		},
		{
			name: "unparsable payloads are relayed unchanged",
			in:   "data: {\"reasoning\"\n\n",
			want: "data: {\"reasoning\"\n\n",
		},
		{
			name: "comments and empty streams survive",
			in:   ": ping\n\n",
			want: ": ping\n\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := io.ReadAll(normalizeChatStream(io.NopCloser(strings.NewReader(tt.in))))
			if err != nil {
				t.Fatalf("read stream: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("stream = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeChatStreamNilIsSafe(t *testing.T) {
	if got := normalizeChatStream(nil); got != nil {
		t.Errorf("normalizeChatStream(nil) = %v, want nil", got)
	}
}

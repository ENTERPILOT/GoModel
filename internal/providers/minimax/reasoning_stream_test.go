package minimax

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

func TestNormalizeChatStream(t *testing.T) {
	tests := []struct {
		name  string
		model string // defaults to a gated model
		in    string
		want  string
	}{
		{
			name: "cumulative details emit only the new suffix and are stripped",
			in: "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_details\":[{\"text\":\"Hel\"},{\"text\":\"lo\"}]}}]}\n\n" +
				"data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_details\":[{\"text\":\"Hello\"},{\"text\":\" world\"}]}}]}\n\n" +
				"data: [DONE]\n\n",
			want: "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"Hello\"},\"index\":0}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\" world\"},\"index\":0}]}\n\n" +
				"data: [DONE]\n\n",
		},
		{
			name: "a repeated cumulative buffer emits an empty delta",
			in: "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_details\":[{\"text\":\"Hi\"}]}}]}\n\n" +
				"data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_details\":[{\"text\":\"Hi\"}]}}]}\n\n",
			want: "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"Hi\"},\"index\":0}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{},\"index\":0}]}\n\n",
		},
		{
			name: "a replaced buffer forwards the whole text",
			in: "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_details\":[{\"text\":\"abc\"}]}}]}\n\n" +
				"data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_details\":[{\"text\":\"xy\"}]}}]}\n\n",
			want: "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"abc\"},\"index\":0}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"xy\"},\"index\":0}]}\n\n",
		},
		{
			name: "choice buffers are independent",
			in: "data: {\"choices\":[{\"index\":1,\"delta\":{\"reasoning_details\":[{\"text\":\"B\"}]}},{\"index\":0,\"delta\":{\"reasoning_details\":[{\"text\":\"A\"}]}}]}\n\n" +
				"data: {\"choices\":[{\"index\":1,\"delta\":{\"reasoning_details\":[{\"text\":\"BC\"}]}}]}\n\n",
			want: "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"B\"},\"index\":1},{\"delta\":{\"reasoning_content\":\"A\"},\"index\":0}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"C\"},\"index\":1}]}\n\n",
		},
		{
			name: "a missing index member falls back to the array position",
			in:   "data: {\"choices\":[{\"delta\":{\"reasoning_details\":[{\"text\":\"C\"}]}}]}\n\n",
			want: "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"C\"}}]}\n\n",
		},
		{
			name: "a non-number index member falls back to the array position",
			in:   "data: {\"choices\":[{\"index\":\"zero\",\"delta\":{\"reasoning_details\":[{\"text\":\"C\"}]}}]}\n\n",
			want: "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"C\"},\"index\":\"zero\"}]}\n\n",
		},
		{
			name: "a delta with reasoning_content is relayed byte for byte",
			in:   "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"We\"}}]}\n\n",
			want: "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"We\"}}]}\n\n",
		},
		{
			name: "a delta carrying both spellings is relayed byte for byte",
			in:   "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_details\":[{\"text\":\"x\"}],\"reasoning_content\":\"keep\"}}]}\n\n",
			want: "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_details\":[{\"text\":\"x\"}],\"reasoning_content\":\"keep\"}}]}\n\n",
		},
		{
			name: "the trailing summary event collapses to its usage",
			in:   "data: {\"id\":\"c\",\"object\":\"chat.completion\",\"choices\":[{\"index\":0,\"message\":{\"role\":\"assistant\",\"content\":\"done\",\"reasoning_details\":[{\"text\":\"all\"}]}}],\"usage\":{\"total_tokens\":9007199254740993}}\n\n",
			want: "data: {\"choices\":[],\"usage\":{\"total_tokens\":9007199254740993}}\n\n",
		},
		{
			name: "a summary event without usage is relayed byte for byte",
			in:   "data: {\"choices\":[{\"message\":{\"role\":\"assistant\"}}]}\n\n",
			want: "data: {\"choices\":[{\"message\":{\"role\":\"assistant\"}}]}\n\n",
		},
		{
			name: "content deltas are relayed byte for byte",
			in:   "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n",
			want: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n",
		},
		{
			name: "a delta without reasoning members is relayed byte for byte",
			in:   "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}],\"reasoning_details\":[]}\n\n",
			want: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}],\"reasoning_details\":[]}\n\n",
		},
		{
			name: "a choice without a delta is relayed byte for byte",
			in:   "data: {\"choices\":[{\"index\":0}],\"reasoning_details\":[]}\n\n",
			want: "data: {\"choices\":[{\"index\":0}],\"reasoning_details\":[]}\n\n",
		},
		{
			name: "a choice that is not an object is relayed unchanged",
			in:   "data: {\"choices\":[1],\"reasoning_details\":[]}\n\n",
			want: "data: {\"choices\":[1],\"reasoning_details\":[]}\n\n",
		},
		{
			name: "payloads without choices are relayed unchanged",
			in:   "data: {\"reasoning_details\":true}\n\n",
			want: "data: {\"reasoning_details\":true}\n\n",
		},
		{
			name: "unparsable deltas are relayed unchanged",
			in:   "data: {\"choices\":[{\"index\":0,\"delta\":\"x\"}],\"reasoning_details\":[]}\n\n",
			want: "data: {\"choices\":[{\"index\":0,\"delta\":\"x\"}],\"reasoning_details\":[]}\n\n",
		},
		{
			name: "reasoning_details of the wrong shape are relayed unchanged",
			in:   "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_details\":{\"text\":\"x\"}}}]}\n\n",
			want: "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_details\":{\"text\":\"x\"}}}]}\n\n",
		},
		{
			name: "unparsable payloads are relayed unchanged",
			in:   "data: {\"reasoning_details\"\n\n",
			want: "data: {\"reasoning_details\"\n\n",
		},
		{
			name: "comments survive",
			in:   ": ping\n\n",
			want: ": ping\n\n",
		},
		{
			name:  "a non-gated model's stream passes through untouched",
			model: "minimax-text",
			in: "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_details\":[{\"text\":\"raw\"}]}}]}\n\n" +
				"data: {\"choices\":[],\"usage\":{}}\n\n",
			want: "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_details\":[{\"text\":\"raw\"}]}}]}\n\n" +
				"data: {\"choices\":[],\"usage\":{}}\n\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := tt.model
			if model == "" {
				model = "minimax-m2"
			}
			got, err := io.ReadAll(normalizeChatStream(io.NopCloser(strings.NewReader(tt.in)), model))
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

// passthroughReader is a *pointer* ReadCloser so tests can pin reader
// identity with require.Same; io.NopCloser returns a struct value.
type passthroughReader struct {
	io.Reader
}

func (*passthroughReader) Close() error { return nil }

func TestNormalizeChatStream_NonGatedModelReturnsTheSameReader(t *testing.T) {
	src := &passthroughReader{Reader: strings.NewReader("data: {}\n\n")}
	require.Same(t, src, normalizeChatStream(src, "minimax-text"))
}

func TestNormalizeChatStreamNilIsSafe(t *testing.T) {
	got := normalizeChatStream(nil, "minimax-m2")
	assert.Nil(t, got)
}

func TestNormalizeChatStreamCloseClosesTheSource(t *testing.T) {
	src := io.NopCloser(strings.NewReader("data: [DONE]\n\n"))
	stream := normalizeChatStream(src, "minimax-m2")
	_, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.NoError(t, stream.Close())
}

func TestStreamChatCompletion_NormalizesReasoningStreamForGatedModel(t *testing.T) {
	upstream := "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n" +
		"data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_details\":[{\"text\":\"Think\"},{\"text\":\"ing\"}]}}]}\n\n" +
		"data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_details\":[{\"text\":\"Thinking hard\"}]}}]}\n\n" +
		"data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Answer\"}}]}\n\n" +
		"data: {\"id\":\"c\",\"object\":\"chat.completion\",\"choices\":[{\"index\":0,\"message\":{\"role\":\"assistant\",\"content\":\"Answer\",\"reasoning_details\":[{\"text\":\"Thinking hard\"}]}}],\"usage\":{\"total_tokens\":21}}\n\n" +
		"data: [DONE]\n\n"
	rewritten := "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"Thinking\"},\"index\":0}],\"id\":\"1\",\"object\":\"chat.completion.chunk\"}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\" hard\"},\"index\":0}],\"id\":\"1\",\"object\":\"chat.completion.chunk\"}\n\n" +
		"data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Answer\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"total_tokens\":21}}\n\n" +
		"data: [DONE]\n\n"

	tests := []struct {
		name       string
		model      string
		wantStream string
		wantSplit  any // reasoning_split the upstream must receive; nil means absent
	}{
		{
			name:       "gated model",
			model:      "MiniMax-M3",
			wantStream: rewritten,
			wantSplit:  true,
		},
		{
			name:       "non-gated model is relayed byte for byte",
			model:      "minimax-text",
			wantStream: upstream,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.SSEServer(t, upstream)
			provider := newTestProvider("minimax-key", server.URL, server.Client(), llmclient.Hooks{})

			stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
				Model:    tt.model,
				Messages: []core.Message{{Role: "user", Content: "hi"}},
			})
			require.NoError(t, err)
			got, err := io.ReadAll(stream)
			require.NoError(t, err)
			require.NoError(t, stream.Close())

			assert.Equal(t, tt.wantStream, string(got))
			raw := capture.Last(t).JSON(t)
			if tt.wantSplit == nil {
				assert.NotContains(t, raw, "reasoning_split")
				return
			}
			assert.Equal(t, tt.wantSplit, raw["reasoning_split"])
		})
	}
}

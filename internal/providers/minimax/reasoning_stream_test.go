package minimax

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// Verbatim data lines from a live MiniMax OpenAI-compatible capture
// (reasoning_split: true). reasoning_content and reasoning_details are both
// incremental; content deltas carry neither member; the stream ends with a
// standard usage-only chunk.
const (
	capturedReasoningDelta1 = `{"id":"07008f4a82fecea87b8c3822c4ed23c0","choices":[{"index":0,"delta":{"role":"assistant","name":"MiniMax AI","audio_content":"","reasoning_content":"The","reasoning_details":[{"type":"reasoning.text","id":"reasoning-text-1","format":"MiniMax-response-v1","index":0,"text":"The"}]}}],"created":1790008394,"model":"MiniMax-M2.7","object":"chat.completion.chunk","usage":null,"input_sensitive":false,"output_sensitive":false,"input_sensitive_type":0,"output_sensitive_type":0,"output_sensitive_int":0}`

	capturedReasoningDelta2 = `{"id":"07008f4a82fecea87b8c3822c4ed23c0","choices":[{"index":0,"delta":{"content":"","role":"assistant","name":"MiniMax AI","audio_content":"","reasoning_content":" user wants a proof that the sum of two odd numbers is even. This is a straightforward number theory proof. I should present it concisely in caveman style.","reasoning_details":[{"type":"reasoning.text","id":"reasoning-text-1","format":"MiniMax-response-v1","index":0,"text":" user wants a proof that the sum of two odd numbers is even. This is a straightforward number theory proof. I should present it concisely in caveman style."}]}}],"created":1790008394,"model":"MiniMax-M2.7","object":"chat.completion.chunk","usage":null,"input_sensitive":false,"output_sensitive":false,"input_sensitive_type":0,"output_sensitive_type":0,"output_sensitive_int":0}`

	capturedContentDelta = `{"id":"07008f4a82fecea87b8c3822c4ed23c0","choices":[{"index":0,"delta":{"content":"**","role":"assistant","name":"MiniMax AI","audio_content":""}}],"created":1790008394,"model":"MiniMax-M2.7","object":"chat.completion.chunk","usage":null,"input_sensitive":false,"output_sensitive":false,"input_sensitive_type":0,"output_sensitive_type":0,"output_sensitive_int":0}`

	capturedFinishChunk = `{"id":"07008f4a82fecea87b8c3822c4ed23c0","choices":[{"finish_reason":"stop","index":0,"delta":{"content":" = 2(a + b + 1)$$\n\nResult divisible by 2. Even. ∎","role":"assistant","name":"MiniMax AI","audio_content":""}}],"created":1790008394,"model":"MiniMax-M2.7","object":"chat.completion.chunk","usage":null,"input_sensitive":false,"output_sensitive":false,"input_sensitive_type":0,"output_sensitive_type":0,"output_sensitive_int":0}`

	capturedUsageChunk = `{"id":"07008f4a82fecea87b8c3822c4ed23c0","choices":[],"created":1790008394,"model":"MiniMax-M2.7","object":"chat.completion.chunk","usage":{"total_tokens":885,"total_characters":0,"prompt_tokens":769,"completion_tokens":116,"completion_tokens_details":{"reasoning_tokens":37},"prompt_tokens_details":{"cached_tokens":443}},"base_resp":{"status_code":0,"status_msg":""}}`
)

// A rewritten chunk is re-encoded from raw members, so it keeps every other
// member's original bytes; the map re-encode sorts keys.
const (
	strippedReasoningDelta1 = `{"choices":[{"delta":{"audio_content":"","name":"MiniMax AI","reasoning_content":"The","role":"assistant"},"index":0}],"created":1790008394,"id":"07008f4a82fecea87b8c3822c4ed23c0","input_sensitive":false,"input_sensitive_type":0,"model":"MiniMax-M2.7","object":"chat.completion.chunk","output_sensitive":false,"output_sensitive_int":0,"output_sensitive_type":0,"usage":null}`

	strippedReasoningDelta2 = `{"choices":[{"delta":{"audio_content":"","content":"","name":"MiniMax AI","reasoning_content":" user wants a proof that the sum of two odd numbers is even. This is a straightforward number theory proof. I should present it concisely in caveman style.","role":"assistant"},"index":0}],"created":1790008394,"id":"07008f4a82fecea87b8c3822c4ed23c0","input_sensitive":false,"input_sensitive_type":0,"model":"MiniMax-M2.7","object":"chat.completion.chunk","output_sensitive":false,"output_sensitive_int":0,"output_sensitive_type":0,"usage":null}`
)

func TestNormalizeChatStream(t *testing.T) {
	tests := []struct {
		name  string
		model string // defaults to a gated model
		in    string
		want  string
	}{
		{
			name: "a reasoning delta keeps reasoning_content and loses reasoning_details",
			in:   "data: " + capturedReasoningDelta1 + "\n\n",
			want: "data: " + strippedReasoningDelta1 + "\n\n",
		},
		{
			name: "the next incremental reasoning delta is rewritten the same way",
			in:   "data: " + capturedReasoningDelta2 + "\n\n",
			want: "data: " + strippedReasoningDelta2 + "\n\n",
		},
		{
			name: "a delta with reasoning_details but no reasoning_content is relayed byte for byte",
			in:   "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\",\"reasoning_details\":{\"text\":\"x\"}}}]}",
			want: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\",\"reasoning_details\":{\"text\":\"x\"}}}]}",
		},
		{
			name: "a content delta is relayed byte for byte",
			in:   "data: " + capturedContentDelta + "\n\n",
			want: "data: " + capturedContentDelta + "\n\n",
		},
		{
			name: "the finish chunk is relayed byte for byte",
			in:   "data: " + capturedFinishChunk + "\n\n",
			want: "data: " + capturedFinishChunk + "\n\n",
		},
		{
			name: "the trailing usage-only chunk is relayed byte for byte",
			in:   "data: " + capturedUsageChunk + "\n\n",
			want: "data: " + capturedUsageChunk + "\n\n",
		},
		{
			name: "[DONE] passes through",
			in:   "data: [DONE]\n\n",
			want: "data: [DONE]\n\n",
		},
		{
			name: "a delta without reasoning_details is relayed byte for byte",
			in:   "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"\",\"reasoning_content\":\"keep\"}}]}",
			want: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"\",\"reasoning_content\":\"keep\"}}]}",
		},
		{
			name: "the marker elsewhere in the envelope rewrites nothing",
			in:   "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}],\"reasoning_details\":[]}\n\n",
			want: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}],\"reasoning_details\":[]}\n\n",
		},
		{
			name: "a bare data prefix without a space is accepted",
			in:   "data:{\"choices\":[{\"index\":0,\"delta\":{\"reasoning_details\":[{\"text\":\"x\"}],\"reasoning_content\":\"y\"}}]}",
			want: "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"y\"},\"index\":0}]}\n",
		},
		{
			name: "an unparsable payload is relayed unchanged",
			in:   "data: {\"reasoning_details\"\n\n",
			want: "data: {\"reasoning_details\"\n\n",
		},
		{
			name: "an unparsable choices member is relayed unchanged",
			in:   "data: {\"choices\":{},\"reasoning_details\":[]}\n\n",
			want: "data: {\"choices\":{},\"reasoning_details\":[]}\n\n",
		},
		{
			name: "a choice that is not an object is relayed unchanged",
			in:   "data: {\"choices\":[1],\"reasoning_details\":[]}\n\n",
			want: "data: {\"choices\":[1],\"reasoning_details\":[]}\n\n",
		},
		{
			name: "a delta that is not an object is relayed unchanged",
			in:   "data: {\"choices\":[{\"index\":0,\"delta\":\"x\"}],\"reasoning_details\":[]}\n\n",
			want: "data: {\"choices\":[{\"index\":0,\"delta\":\"x\"}],\"reasoning_details\":[]}\n\n",
		},
		{
			name: "a choice without a delta is relayed byte for byte",
			in:   "data: {\"choices\":[{\"index\":0}],\"reasoning_details\":[]}\n\n",
			want: "data: {\"choices\":[{\"index\":0}],\"reasoning_details\":[]}\n\n",
		},
		{
			name: "comments survive",
			in:   ": ping\n\n",
			want: ": ping\n\n",
		},
		{
			name:  "a non-gated model's stream passes through untouched",
			model: "minimax-text",
			in:    "data: " + capturedReasoningDelta1 + "\n\n",
			want:  "data: " + capturedReasoningDelta1 + "\n\n",
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

// capturedStreamHead is the two reasoning events of the live capture; its
// tail is everything from the first content delta to the final usage chunk.
const capturedStreamTail = `data: {"id":"07008f4a82fecea87b8c3822c4ed23c0","choices":[{"index":0,"delta":{"content":"**","role":"assistant","name":"MiniMax AI","audio_content":""}}],"created":1790008394,"model":"MiniMax-M2.7","object":"chat.completion.chunk","usage":null,"input_sensitive":false,"output_sensitive":false,"input_sensitive_type":0,"output_sensitive_type":0,"output_sensitive_int":0}

data: {"id":"07008f4a82fecea87b8c3822c4ed23c0","choices":[{"index":0,"delta":{"content":"Proof.**\n\nOdd numbers: $2a + 1$ and $2b + 1$ for","role":"assistant","name":"MiniMax AI","audio_content":""}}],"created":1790008394,"model":"MiniMax-M2.7","object":"chat.completion.chunk","usage":null,"input_sensitive":false,"output_sensitive":false,"input_sensitive_type":0,"output_sensitive_type":0,"output_sensitive_int":0}

data: {"id":"07008f4a82fecea87b8c3822c4ed23c0","choices":[{"index":0,"delta":{"content":" integers $a, b$.\n\nAdd:\n\n$$(2a + 1) + (2b + 1) = 2a + 2b + 2","role":"assistant","name":"MiniMax AI","audio_content":""}}],"created":1790008394,"model":"MiniMax-M2.7","object":"chat.completion.chunk","usage":null,"input_sensitive":false,"output_sensitive":false,"input_sensitive_type":0,"output_sensitive_type":0,"output_sensitive_int":0}

data: {"id":"07008f4a82fecea87b8c3822c4ed23c0","choices":[{"finish_reason":"stop","index":0,"delta":{"content":" = 2(a + b + 1)$$\n\nResult divisible by 2. Even. ∎","role":"assistant","name":"MiniMax AI","audio_content":""}}],"created":1790008394,"model":"MiniMax-M2.7","object":"chat.completion.chunk","usage":null,"input_sensitive":false,"output_sensitive":false,"input_sensitive_type":0,"output_sensitive_type":0,"output_sensitive_int":0}

data: {"id":"07008f4a82fecea87b8c3822c4ed23c0","choices":[],"created":1790008394,"model":"MiniMax-M2.7","object":"chat.completion.chunk","usage":{"total_tokens":885,"total_characters":0,"prompt_tokens":769,"completion_tokens":116,"completion_tokens_details":{"reasoning_tokens":37},"prompt_tokens_details":{"cached_tokens":443}},"base_resp":{"status_code":0,"status_msg":""}}

`

func TestStreamChatCompletion_StripsReasoningDetailsForGatedModel(t *testing.T) {
	upstream := "data: " + capturedReasoningDelta1 + "\n\ndata: " + capturedReasoningDelta2 + "\n\n" + capturedStreamTail

	tests := []struct {
		name       string
		model      string
		wantStream string
		wantSplit  any // reasoning_split the upstream must receive; nil means absent
	}{
		{
			name:       "gated model loses reasoning_details and keeps the rest",
			model:      "MiniMax-M2.7",
			wantStream: "data: " + strippedReasoningDelta1 + "\n\ndata: " + strippedReasoningDelta2 + "\n\n" + capturedStreamTail,
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

// With a caller-set reasoning_split the stream still reaches the strip pass,
// so a delta carrying reasoning_details without reasoning_content must come
// out byte for byte: the array is then the only copy of the reasoning.
func TestStreamChatCompletion_CallerSplitFalseRelaysDetailsOnlyDeltas(t *testing.T) {
	upstream := "data: {\"id\":\"07008f4a82fecea87b8c3822c4ed23c0\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"\",\"reasoning_details\":[{\"type\":\"reasoning.text\",\"text\":\"only copy\"}]}}],\"created\":1790008394,\"model\":\"MiniMax-M2.7\",\"object\":\"chat.completion.chunk\",\"usage\":null}\n\ndata: [DONE]\n\n"
	server, capture := providertest.SSEServer(t, upstream)
	provider := newTestProvider("minimax-key", server.URL, server.Client(), llmclient.Hooks{})

	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "MiniMax-M2.7",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			reasoningSplitKey: json.RawMessage(`false`),
		}),
	})
	require.NoError(t, err)
	got, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.NoError(t, stream.Close())

	assert.Equal(t, upstream, string(got))
	raw := capture.Last(t).JSON(t)
	assert.Equal(t, false, raw[reasoningSplitKey], "the caller's reasoning_split must reach upstream unchanged")
}

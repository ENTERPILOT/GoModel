package responsecache

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractEmbedText_ResponsesInputArray(t *testing.T) {
	body := []byte(`{
  "model": "claude-opus-4-6",
  "reasoning": {"effort": "medium"},
  "input": [
    {"role": "user", "content": "what is the capital of Germany"}
  ]
}`)
	text, n := extractEmbedText(body, false)
	require.Equal(t, "what is the capital of Germany", text)
	require.Equal(t, 1, n)
}

func TestExtractEmbedText_InputString(t *testing.T) {
	body := []byte(`{"model":"x","input":"hello"}`)
	text, n := extractEmbedText(body, false)
	require.Equal(t, "hello", text)
	require.Equal(t, 1, n)
}

func TestConversationInvariantFingerprint_ResponsesInputArray(t *testing.T) {
	body := []byte(`{"input":[{"role":"user","content":"same"}]}`)
	fp, ok := conversationInvariantFingerprint(body, false)
	require.True(t, ok)
	require.NotEmpty(t, fp)
}

func TestConversationInvariantFingerprint_InputString(t *testing.T) {
	body := []byte(`{"input":"hi"}`)
	fp, ok := conversationInvariantFingerprint(body, false)
	require.True(t, ok)
	require.Empty(t, fp)
}

func TestComputeParamsHash_IncludesReasoning(t *testing.T) {
	low := []byte(`{"model":"m","reasoning":{"effort":"low"}}`)
	high := []byte(`{"model":"m","reasoning":{"effort":"high"}}`)
	h1 := computeParamsHash(low, "/v1/responses", nil, "", "embed")
	h2 := computeParamsHash(high, "/v1/responses", nil, "", "embed")
	require.NotEqual(t, h2, h1)
}

func TestComputeParamsHash_IncludesMaxCompletionTokens(t *testing.T) {
	low := computeParamsHash([]byte(`{"model":"m","max_completion_tokens":50}`), "/v1/chat/completions", nil, "", "embed")
	high := computeParamsHash([]byte(`{"model":"m","max_completion_tokens":5000}`), "/v1/chat/completions", nil, "", "embed")
	unset := computeParamsHash([]byte(`{"model":"m"}`), "/v1/chat/completions", nil, "", "embed")
	require.NotEqual(t, low, high)
	require.NotEqual(t, low, unset)

	// A request without it hashes exactly as before the field was added,
	// so existing semantic-cache entries stay reachable.
	before := computeParamsHash([]byte(`{"model":"m","max_tokens":50,"temperature":0.2}`), "/v1/chat/completions", nil, "", "embed")
	require.Equal(t, "0c5fafbab6973aa7f7b38cc1a9fb55ce1e4bf7d45fabf69c3529996a70608fce", before)
}

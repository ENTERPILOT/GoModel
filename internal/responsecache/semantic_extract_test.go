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
	h1 := computeParamsHash(low, "/v1/responses", nil, "", "embed", false)
	h2 := computeParamsHash(high, "/v1/responses", nil, "", "embed", false)
	require.NotEqual(t, h2, h1)
}

// Virtual-model params land in the prepared body the cache hashes; a forced
// response_format arrives on the Responses API as text.format, so a policy
// that changes it must change the key.
func TestComputeParamsHash_ResponsesTextFormatChangesHash(t *testing.T) {
	plain := computeParamsHash([]byte(`{"model":"m","input":"hi","text":{"format":{"type":"text"}}}`), responsesPath, nil, "", "embed", false)
	schema := computeParamsHash([]byte(`{"model":"m","input":"hi","text":{"format":{"type":"json_schema","name":"s","schema":{"type":"object"}}}}`), responsesPath, nil, "", "embed", false)
	require.NotEqual(t, plain, schema)
}

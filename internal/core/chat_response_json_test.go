package core

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChatResponseJSON_RoundTripsUnknownMembers(t *testing.T) {
	body := []byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"gpt-4o-mini","provider":"upstream","created":1,` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":"Hi","annotations":[]},"finish_reason":"stop","native_finish_reason":"end_turn"}],` +
		`"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"cost":0.0001},"citations":["https://example.com"]}`)

	var resp ChatResponse
	err := json.Unmarshal(body, &resp)
	require.NoError(t, err)
	got := string(lookupUnknownField(t, resp.ExtraFields, "citations"))
	require.Equal(t, `["https://example.com"]`, got)
	require.Len(t, resp.Choices, 1)
	got = string(lookupUnknownField(t, resp.Choices[0].ExtraFields, "native_finish_reason"))
	require.Equal(t, `"end_turn"`, got)
	require.Equal(t, "stop", resp.Choices[0].FinishReason)
	require.Equal(t, 3, resp.Usage.TotalTokens, "typed fields lost: %+v", resp)

	// The gateway overwrites the provider member the way the translated path
	// does, so the re-encoded body must carry that value, not the upstream's.
	resp.Provider = "openai"
	encoded, err := json.Marshal(resp)
	require.NoError(t, err)

	for _, want := range []string{`"provider":"openai"`, `"citations":["https://example.com"]`, `"native_finish_reason":"end_turn"`, `"cost":0.0001`, `"annotations":[]`} {
		require.Contains(t, string(encoded), string([]byte(want)))
	}
	require.False(t, bytes.Contains(encoded, []byte(`"upstream"`)), "upstream provider member survived re-encoding:\n%s", encoded)
}

func TestChatResponseJSON_NullUsageAndChoiceDecode(t *testing.T) {
	var resp ChatResponse
	err := json.Unmarshal([]byte(`{"id":"x","choices":[null],"usage":null}`), &resp)
	require.NoError(t, err)
	require.True(t, resp.ExtraFields.IsEmpty())
	require.Len(t, resp.Choices, 1)
	require.True(t, resp.Choices[0].ExtraFields.IsEmpty(), "unexpected extras on null members: %+v", resp)
}

func TestChatResponseJSON_RejectsMalformedInput(t *testing.T) {
	var resp ChatResponse
	require.Error(t, json.Unmarshal([]byte(`{"id":`), &resp))

	var choice Choice
	require.Error(t, json.Unmarshal([]byte(`{"index":"x"}`), &choice))
	require.Error(t, json.Unmarshal([]byte(`{"choices":[{"index":"x"}]}`), &resp))
}

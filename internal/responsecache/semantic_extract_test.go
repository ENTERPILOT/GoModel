package responsecache

import (
	"testing"

	"github.com/stretchr/testify/assert"
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

func TestConversationInvariantFingerprint_CoversEarlierTurnsInFull(t *testing.T) {
	const base = `{"messages":[
		{"role":"user","content":"weather?"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"Sunny"},
		{"role":"user","name":"alice","content":"and tomorrow?"}
	]}`
	tests := []struct {
		name string
		body string
		same bool
	}{
		{name: "last user text", same: true, body: `{"messages":[
			{"role":"user","content":"weather?"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"Sunny"},
			{"role":"user","name":"alice","content":"what about tomorrow?"}
		]}`},
		{name: "member order and whitespace", same: true, body: `{"messages":[
			{"content":"weather?","role":"user"},
			{"tool_calls":[{"function":{"arguments":"{\"city\":\"Paris\"}","name":"get_weather"},"type":"function","id":"call_1"}],"content":null,"role":"assistant"},
			{"content":"Sunny", "role":"tool", "tool_call_id":"call_1"},
			{"content":"and tomorrow?","name":"alice","role":"user"}
		]}`},
		{name: "tool call arguments", body: `{"messages":[
			{"role":"user","content":"weather?"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Oslo\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"Sunny"},
			{"role":"user","name":"alice","content":"and tomorrow?"}
		]}`},
		{name: "tool_call_id", body: `{"messages":[
			{"role":"user","content":"weather?"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]},
			{"role":"tool","tool_call_id":"call_2","content":"Sunny"},
			{"role":"user","name":"alice","content":"and tomorrow?"}
		]}`},
		{name: "earlier user text", body: `{"messages":[
			{"role":"user","content":"news?"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"Sunny"},
			{"role":"user","name":"alice","content":"and tomorrow?"}
		]}`},
		{name: "last user name", body: `{"messages":[
			{"role":"user","content":"weather?"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"Sunny"},
			{"role":"user","name":"bob","content":"and tomorrow?"}
		]}`},
	}
	want, ok := conversationInvariantFingerprint([]byte(base), false)
	require.True(t, ok)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := conversationInvariantFingerprint([]byte(tt.body), false)
			require.True(t, ok)
			if tt.same {
				assert.Equal(t, want, got)
			} else {
				assert.NotEqual(t, want, got)
			}
		})
	}
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

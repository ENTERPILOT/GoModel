package requestparams

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func chatRequest(t *testing.T, body string) *core.ChatRequest {
	t.Helper()
	var req core.ChatRequest
	require.NoError(t, json.Unmarshal([]byte(body), &req))
	return &req
}

func encode(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return string(data)
}

func TestApplyChat(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		defaults Set
		params   Set
		want     string
	}{
		{
			name:     "defaults fill only missing typed fields",
			body:     `{"model":"m","messages":[],"temperature":0.2}`,
			defaults: Set{"temperature": raw("1"), "top_p": raw("0.95"), "max_tokens": raw("64"), "user": raw(`"ops"`)},
			want:     `{"model":"m","messages":[],"temperature":0.2,"top_p":0.95,"max_tokens":64,"user":"ops"}`,
		},
		{
			name:   "params replace typed fields",
			body:   `{"model":"m","messages":[],"temperature":0.2,"max_tokens":10,"parallel_tool_calls":true}`,
			params: Set{"temperature": raw("1"), "max_tokens": raw("64"), "parallel_tool_calls": raw("false"), "service_tier": raw(`"flex"`)},
			want:   `{"model":"m","messages":[],"temperature":1,"max_tokens":64,"parallel_tool_calls":false,"service_tier":"flex"}`,
		},
		{
			name:   "null strips typed and unknown fields",
			body:   `{"model":"m","messages":[],"temperature":0.2,"top_k":5,"user":"u"}`,
			params: Set{"temperature": nil, "top_k": nil, "user": nil, "min_p": nil},
			want:   `{"model":"m","messages":[]}`,
		},
		{
			name:     "defaults fill missing unknown fields and keep client ones",
			body:     `{"model":"m","messages":[],"top_k":5}`,
			defaults: Set{"top_k": raw("20"), "min_p": raw("0")},
			want:     `{"model":"m","messages":[],"top_k":5,"min_p":0}`,
		},
		{
			name:   "params replace unknown scalars",
			body:   `{"model":"m","messages":[],"top_k":5}`,
			params: Set{"top_k": raw("20"), "repetition_penalty": raw("1")},
			want:   `{"model":"m","messages":[],"top_k":20,"repetition_penalty":1}`,
		},
		{
			name:   "forced objects merge one level with the operator winning",
			body:   `{"model":"m","messages":[],"chat_template_kwargs":{"enable_thinking":false,"keep":1}}`,
			params: Set{"chat_template_kwargs": raw(`{"enable_thinking":true,"preserve_thinking":true}`)},
			want:   `{"model":"m","messages":[],"chat_template_kwargs":{"enable_thinking":true,"keep":1,"preserve_thinking":true}}`,
		},
		{
			name:     "default objects merge one level with the client winning",
			body:     `{"model":"m","messages":[],"chat_template_kwargs":{"enable_thinking":false}}`,
			defaults: Set{"chat_template_kwargs": raw(`{"enable_thinking":true,"preserve_thinking":true}`)},
			want:     `{"model":"m","messages":[],"chat_template_kwargs":{"enable_thinking":false,"preserve_thinking":true}}`,
		},
		{
			name:   "a forced object replaces a client scalar",
			body:   `{"model":"m","messages":[],"chat_template_kwargs":"bad"}`,
			params: Set{"chat_template_kwargs": raw(`{"enable_thinking":true}`)},
			want:   `{"model":"m","messages":[],"chat_template_kwargs":{"enable_thinking":true}}`,
		},
		{
			name:     "a default object leaves a client scalar alone",
			body:     `{"model":"m","messages":[],"chat_template_kwargs":"client"}`,
			defaults: Set{"chat_template_kwargs": raw(`{"enable_thinking":true}`)},
			want:     `{"model":"m","messages":[],"chat_template_kwargs":"client"}`,
		},
		{
			name:     "a forced value layered over a default on the same key wins",
			body:     `{"model":"m","messages":[]}`,
			defaults: Set{"top_k": raw("20"), "chat_template_kwargs": raw(`{"a":1}`)},
			params:   Set{"top_k": raw("5"), "chat_template_kwargs": raw(`{"b":2}`)},
			want:     `{"model":"m","messages":[],"top_k":5,"chat_template_kwargs":{"a":1,"b":2}}`,
		},
		{
			name:   "tool_choice accepts an object",
			body:   `{"model":"m","messages":[],"tool_choice":"auto"}`,
			params: Set{"tool_choice": raw(`{"type":"function","function":{"name":"f"}}`)},
			want:   `{"model":"m","messages":[],"tool_choice":{"type":"function","function":{"name":"f"}}}`,
		},
		{
			name:   "arrays replace rather than merge",
			body:   `{"model":"m","messages":[],"stop":["a"]}`,
			params: Set{"stop": raw(`["b","c"]`)},
			want:   `{"model":"m","messages":[],"stop":["b","c"]}`,
		},
		{
			name:   "a forced token limit strips the client's other spellings",
			body:   `{"model":"m","messages":[],"max_completion_tokens":2000,"max_output_tokens":3000}`,
			params: Set{"max_tokens": raw("50")},
			want:   `{"model":"m","messages":[],"max_tokens":50}`,
		},
		{
			name:     "a default token limit leaves any client spelling alone",
			body:     `{"model":"m","messages":[],"max_completion_tokens":2000}`,
			defaults: Set{"max_tokens": raw("50")},
			want:     `{"model":"m","messages":[],"max_completion_tokens":2000}`,
		},
		{
			name:   "every token-limit spelling sets max_tokens",
			body:   `{"model":"m","messages":[]}`,
			params: Set{"max_output_tokens": raw("70")},
			want:   `{"model":"m","messages":[],"max_tokens":70}`,
		},
		{
			name:   "a forced max_completion_tokens keeps its spelling and strips the others",
			body:   `{"model":"m","messages":[],"max_tokens":10,"max_output_tokens":3000}`,
			params: Set{"max_completion_tokens": raw("50")},
			want:   `{"model":"m","messages":[],"max_completion_tokens":50}`,
		},
		{
			name:     "a default max_completion_tokens leaves a client max_tokens alone",
			body:     `{"model":"m","messages":[],"max_tokens":10}`,
			defaults: Set{"max_completion_tokens": raw("50")},
			want:     `{"model":"m","messages":[],"max_tokens":10}`,
		},
		{
			name:   "a forced null still strips a Responses-only key",
			body:   `{"model":"m","messages":[],"truncation":"auto"}`,
			params: Set{"truncation": nil},
			want:   `{"model":"m","messages":[]}`,
		},
		{
			name:     "Responses-only keys are skipped",
			body:     `{"model":"m","messages":[]}`,
			defaults: Set{"truncation": raw(`"auto"`), "include": raw(`["x"]`), "text": raw(`{"verbosity":"low"}`), "seed": raw("7")},
			want:     `{"model":"m","messages":[],"seed":7}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := chatRequest(t, tt.body)
			before := encode(t, original)
			got, err := ApplyChat(original, tt.defaults, tt.params)
			require.NoError(t, err)
			assert.JSONEq(t, tt.want, encode(t, got))
			assert.JSONEq(t, before, encode(t, original), "the client request must not be mutated")
		})
	}
}

func TestApplyResponsesDoesNotShareMapsWithClient(t *testing.T) {
	original := responsesRequest(t, `{"model":"m","input":"hi","metadata":{"a":"1"},"include":["y"],"tools":[{"type":"function"}]}`)
	got, err := ApplyResponses(original, nil, Set{"metadata": raw(`{"b":"2"}`)})
	require.NoError(t, err)
	got.Metadata["c"] = "3"
	got.Include[0] = "changed"
	assert.Equal(t, map[string]string{"a": "1"}, original.Metadata)
	assert.Equal(t, []string{"y"}, original.Include)
	assert.Equal(t, original.Tools, got.Tools, "frozen members stay as the client sent them")
}

func TestApplyResponsesTypedMergeKeepsFrozenBody(t *testing.T) {
	body := `{"model":"m","input":[{"role":"user","content":"hi"}],"instructions":"be brief","tools":[{"type":"function","name":"f"}],"prompt":{"id":"p1"},"metadata":{"a":"1"}}`
	original := responsesRequest(t, body)
	got, err := ApplyResponses(original, nil, Set{"metadata": raw(`{"b":"2"}`)})
	require.NoError(t, err)
	want := `{"model":"m","input":[{"role":"user","content":"hi"}],"instructions":"be brief","tools":[{"type":"function","name":"f"}],"prompt":{"id":"p1"},"metadata":{"a":"1","b":"2"}}`
	assert.JSONEq(t, want, encode(t, got))
	assert.JSONEq(t, body, encode(t, original), "the client request must not be mutated")
}

func TestApplyChatNoopReturnsSameRequest(t *testing.T) {
	req := chatRequest(t, `{"model":"m","messages":[]}`)
	got, err := ApplyChat(req, nil, nil)
	require.NoError(t, err)
	assert.Same(t, req, got)
	nilReq, err := ApplyChat(nil, Set{"top_k": raw("1")}, nil)
	require.NoError(t, err)
	assert.Nil(t, nilReq)
}

func TestApplyChatKeepsMessagesAndCachePlan(t *testing.T) {
	req := chatRequest(t, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	req.PromptCachePlan = &core.PromptCachePlan{}
	got, err := ApplyChat(req, nil, Set{"temperature": raw("1")})
	require.NoError(t, err)
	assert.Equal(t, req.Messages, got.Messages)
	assert.Same(t, req.PromptCachePlan, got.PromptCachePlan)
}

func TestApplyChatRejectsWrongTypes(t *testing.T) {
	req := chatRequest(t, `{"model":"m","messages":[]}`)
	_, err := ApplyChat(req, nil, Set{"temperature": raw(`"hot"`)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `param "temperature"`)
}

func responsesRequest(t *testing.T, body string) *core.ResponsesRequest {
	t.Helper()
	var req core.ResponsesRequest
	require.NoError(t, json.Unmarshal([]byte(body), &req))
	return &req
}

func TestApplyResponses(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		defaults Set
		params   Set
		want     string
	}{
		{
			name:     "max_tokens maps to max_output_tokens and defaults respect client values",
			body:     `{"model":"m","input":"hi","max_output_tokens":10}`,
			defaults: Set{"max_tokens": raw("64"), "temperature": raw("1")},
			want:     `{"model":"m","input":"hi","max_output_tokens":10,"temperature":1}`,
		},
		{
			name:   "forced typed fields and unknown fields",
			body:   `{"model":"m","input":"hi","temperature":0.1,"top_k":3}`,
			params: Set{"max_tokens": raw("64"), "temperature": raw("1"), "top_k": raw("20"), "store": raw("false"), "truncation": raw(`"auto"`)},
			want:   `{"model":"m","input":"hi","max_output_tokens":64,"temperature":1,"top_k":20,"store":false,"truncation":"auto"}`,
		},
		{
			name:   "typed fields outside the switch go through the JSON merge, objects one level deep",
			body:   `{"model":"m","input":[{"role":"user","content":"hi"}],"metadata":{"a":"1","b":"old"},"include":["y"]}`,
			params: Set{"metadata": raw(`{"b":"2"}`), "include": raw(`["reasoning.encrypted_content"]`)},
			want:   `{"model":"m","input":[{"role":"user","content":"hi"}],"metadata":{"a":"1","b":"2"},"include":["reasoning.encrypted_content"]}`,
		},
		{
			name:     "defaults on JSON-merged typed fields only fill missing members",
			body:     `{"model":"m","input":"hi","metadata":{"a":"1"},"include":["y"]}`,
			defaults: Set{"metadata": raw(`{"a":"ignored","b":"2"}`), "include": raw(`["x"]`), "store": raw("true")},
			want:     `{"model":"m","input":"hi","metadata":{"a":"1","b":"2"},"include":["y"],"store":true}`,
		},
		{
			name:   "null strips a JSON-merged typed field",
			body:   `{"model":"m","input":"hi","metadata":{"a":"1"}}`,
			params: Set{"metadata": nil},
			want:   `{"model":"m","input":"hi"}`,
		},
		{
			name:   "a forced token limit strips the client's chat spellings",
			body:   `{"model":"m","input":"hi","max_output_tokens":10,"max_completion_tokens":2000}`,
			params: Set{"max_completion_tokens": raw("50")},
			want:   `{"model":"m","input":"hi","max_output_tokens":50}`,
		},
		{
			name:   "a forced null still strips a chat-only key",
			body:   `{"model":"m","input":"hi","stop":["x"]}`,
			params: Set{"stop": nil},
			want:   `{"model":"m","input":"hi"}`,
		},
		{
			name:   "a forced response_format lands on top of a forced text object",
			body:   `{"model":"m","input":"hi"}`,
			params: Set{"text": raw(`{"format":{"type":"text"},"verbosity":"low"}`), "response_format": raw(`{"type":"json_object"}`)},
			want:   `{"model":"m","input":"hi","text":{"format":{"type":"json_object"},"verbosity":"low"}}`,
		},
		{
			name:     "a default text object fills only missing members",
			body:     `{"model":"m","input":"hi","text":{"verbosity":"high"}}`,
			defaults: Set{"text": raw(`{"verbosity":"low","format":{"type":"text"}}`)},
			want:     `{"model":"m","input":"hi","text":{"verbosity":"high","format":{"type":"text"}}}`,
		},
		{
			name:     "a response_format type Responses lacks is skipped",
			body:     `{"model":"m","input":"hi"}`,
			defaults: Set{"response_format": raw(`{"type":"regex","pattern":"a+"}`)},
			want:     `{"model":"m","input":"hi"}`,
		},
		{
			name:     "chat-only keys are skipped",
			body:     `{"model":"m","input":"hi"}`,
			defaults: Set{"seed": raw("7"), "stop": raw(`["x"]`), "frequency_penalty": raw("0.5"), "top_k": raw("20")},
			want:     `{"model":"m","input":"hi","top_k":20}`,
		},
		{
			name:     "response_format and verbosity land in text, defaults keeping client members",
			body:     `{"model":"m","input":"hi","text":{"verbosity":"high"}}`,
			defaults: Set{"verbosity": raw(`"low"`), "response_format": raw(`{"type":"json_schema","json_schema":{"name":"s","schema":{"type":"object"},"strict":true}}`)},
			want:     `{"model":"m","input":"hi","text":{"verbosity":"high","format":{"type":"json_schema","name":"s","schema":{"type":"object"},"strict":true}}}`,
		},
		{
			name:   "forced text members replace and null removes",
			body:   `{"model":"m","input":"hi","text":{"verbosity":"high","format":{"type":"text"}}}`,
			params: Set{"verbosity": raw(`"low"`), "response_format": nil},
			want:   `{"model":"m","input":"hi","text":{"verbosity":"low"}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := responsesRequest(t, tt.body)
			before := encode(t, original)
			got, err := ApplyResponses(original, tt.defaults, tt.params)
			require.NoError(t, err)
			assert.JSONEq(t, tt.want, encode(t, got))
			assert.JSONEq(t, before, encode(t, original), "the client request must not be mutated")
			assert.Equal(t, original.Input, got.Input, "input keeps its decoded shape")
		})
	}
}

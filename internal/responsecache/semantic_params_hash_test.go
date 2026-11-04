package responsecache

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func paramsHashOf(body, path string, excludeSystem bool) string {
	return computeParamsHash([]byte(body), path, nil, "", "embed", excludeSystem)
}

func TestComputeParamsHash_OutputShapingFieldsChangeHash(t *testing.T) {
	const (
		chatBase      = `{"model":"m","messages":[{"role":"user","content":"same"}]}`
		responsesBase = `{"model":"m","input":"same"}`
		messagesBase  = `{"model":"m","max_tokens":64,"messages":[{"role":"user","content":"same"}]}`
	)
	tests := []struct {
		name    string
		path    string
		base    string
		changed string
	}{
		{name: "chat stop", path: "/v1/chat/completions", base: chatBase, changed: `{"model":"m","stop":["\n"],"messages":[{"role":"user","content":"same"}]}`},
		{name: "chat seed", path: "/v1/chat/completions", base: chatBase, changed: `{"model":"m","seed":7,"messages":[{"role":"user","content":"same"}]}`},
		{name: "chat n", path: "/v1/chat/completions", base: chatBase, changed: `{"model":"m","n":3,"messages":[{"role":"user","content":"same"}]}`},
		{name: "chat tool_choice", path: "/v1/chat/completions", base: chatBase, changed: `{"model":"m","tool_choice":"required","messages":[{"role":"user","content":"same"}]}`},
		{name: "chat parallel_tool_calls", path: "/v1/chat/completions", base: chatBase, changed: `{"model":"m","parallel_tool_calls":false,"messages":[{"role":"user","content":"same"}]}`},
		{name: "chat frequency_penalty", path: "/v1/chat/completions", base: chatBase, changed: `{"model":"m","frequency_penalty":1,"messages":[{"role":"user","content":"same"}]}`},
		{name: "chat presence_penalty", path: "/v1/chat/completions", base: chatBase, changed: `{"model":"m","presence_penalty":1,"messages":[{"role":"user","content":"same"}]}`},
		{name: "chat logit_bias", path: "/v1/chat/completions", base: chatBase, changed: `{"model":"m","logit_bias":{"50256":-100},"messages":[{"role":"user","content":"same"}]}`},
		{name: "chat logprobs", path: "/v1/chat/completions", base: chatBase, changed: `{"model":"m","logprobs":true,"top_logprobs":2,"messages":[{"role":"user","content":"same"}]}`},
		{name: "chat max_completion_tokens", path: "/v1/chat/completions", base: chatBase, changed: `{"model":"m","max_completion_tokens":16,"messages":[{"role":"user","content":"same"}]}`},
		{name: "chat reasoning_effort", path: "/v1/chat/completions", base: chatBase, changed: `{"model":"m","reasoning_effort":"high","messages":[{"role":"user","content":"same"}]}`},
		{name: "chat unknown field", path: "/v1/chat/completions", base: chatBase, changed: `{"model":"m","future_knob":1,"messages":[{"role":"user","content":"same"}]}`},
		{name: "responses text format", path: "/v1/responses", base: responsesBase, changed: `{"model":"m","input":"same","text":{"format":{"type":"json_object"}}}`},
		{name: "responses tool_choice", path: "/v1/responses", base: responsesBase, changed: `{"model":"m","input":"same","tool_choice":"none"}`},
		{name: "responses truncation", path: "/v1/responses", base: responsesBase, changed: `{"model":"m","input":"same","truncation":"auto"}`},
		{name: "responses instructions", path: "/v1/responses", base: responsesBase, changed: `{"model":"m","input":"same","instructions":"be terse"}`},
		{name: "messages system", path: "/v1/messages", base: messagesBase, changed: `{"model":"m","max_tokens":64,"system":"be terse","messages":[{"role":"user","content":"same"}]}`},
		{name: "messages thinking", path: "/v1/messages", base: messagesBase, changed: `{"model":"m","max_tokens":64,"thinking":{"type":"enabled","budget_tokens":32},"messages":[{"role":"user","content":"same"}]}`},
		{name: "messages stop_sequences", path: "/v1/messages", base: messagesBase, changed: `{"model":"m","max_tokens":64,"stop_sequences":["END"],"messages":[{"role":"user","content":"same"}]}`},
		{name: "messages top_k", path: "/v1/messages", base: messagesBase, changed: `{"model":"m","max_tokens":64,"top_k":5,"messages":[{"role":"user","content":"same"}]}`},
		{name: "seed beyond float64 precision", path: "/v1/chat/completions", base: `{"model":"m","seed":9007199254740992,"messages":[]}`, changed: `{"model":"m","seed":9007199254740993,"messages":[]}`},
		{name: "key holding a NUL is not two members", path: "/v1/chat/completions", base: `{"model":"m","temperature":0.5,"top_p":1,"messages":[]}`, changed: `{"model":"m","temperature\u00000.5\u0000top_p":1,"messages":[]}`},
		{name: "responses metadata is echoed", path: "/v1/responses", base: responsesBase, changed: `{"model":"m","input":"same","metadata":{"team":"a"}}`},
		{name: "responses user is echoed", path: "/v1/responses", base: responsesBase, changed: `{"model":"m","input":"same","user":"u1"}`},
		{name: "responses safety_identifier is echoed", path: "/v1/responses", base: responsesBase, changed: `{"model":"m","input":"same","safety_identifier":"s1"}`},
		{name: "responses prompt_cache_key is echoed", path: "/v1/responses", base: responsesBase, changed: `{"model":"m","input":"same","prompt_cache_key":"k"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotEqual(t, paramsHashOf(tt.base, tt.path, false), paramsHashOf(tt.changed, tt.path, false))
		})
	}
}

func TestComputeParamsHash_IgnoresNonShapingDifferences(t *testing.T) {
	const path = "/v1/chat/completions"
	tests := []struct {
		name  string
		path  string
		first string
		other string
	}{
		{name: "messages caller identity", path: "/v1/messages", first: `{"model":"m","max_tokens":8,"messages":[]}`, other: `{"model":"m","max_tokens":8,"metadata":{"user_id":"u1"},"messages":[]}`},
		{name: "member order and whitespace", first: `{"model":"m","temperature":0.5,"messages":[]}`, other: "{ \"messages\": [],\n \"temperature\": 0.5, \"model\": \"m\" }"},
		{name: "nested member order", first: `{"model":"m","response_format":{"type":"json_schema","json_schema":{"name":"a","strict":true}}}`, other: `{"model":"m","response_format":{"json_schema":{"strict":true,"name":"a"},"type":"json_schema"}}`},
		{name: "prompt text", first: `{"model":"m","messages":[{"role":"user","content":"hello"}]}`, other: `{"model":"m","messages":[{"role":"user","content":"hi there"}]}`},
		{name: "responses input", first: `{"model":"m","input":"hello"}`, other: `{"model":"m","input":[{"role":"user","content":"hi"}]}`},
		{name: "caller identity", first: `{"model":"m","messages":[]}`, other: `{"model":"m","user":"u1","safety_identifier":"s1","metadata":{"team":"a"},"messages":[]}`},
		{name: "provider prompt cache hints", first: `{"model":"m","messages":[]}`, other: `{"model":"m","prompt_cache_key":"k","prompt_cache_retention":"24h","messages":[]}`},
		{name: "stream false is absent", first: `{"model":"m","messages":[]}`, other: `{"model":"m","stream":false,"messages":[]}`},
		{name: "stream_options without stream", first: `{"model":"m","messages":[]}`, other: `{"model":"m","stream_options":{"include_usage":true},"messages":[]}`},
		{name: "tool order", first: `{"model":"m","tools":[{"type":"function","function":{"name":"a"}},{"type":"function","function":{"name":"b"}}]}`, other: `{"model":"m","tools":[{"type":"function","function":{"name":"b"}},{"type":"function","function":{"name":"a"}}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := path
			if tt.path != "" {
				p = tt.path
			}
			assert.Equal(t, paramsHashOf(tt.first, p, false), paramsHashOf(tt.other, p, false))
		})
	}
}

func TestComputeParamsHash_TopLevelSystemFollowsExcludeSystem(t *testing.T) {
	const (
		path  = "/v1/messages"
		terse = `{"model":"m","system":"be terse","messages":[{"role":"user","content":"same"}]}`
		long  = `{"model":"m","system":"be thorough","messages":[{"role":"user","content":"same"}]}`
	)
	assert.NotEqual(t, paramsHashOf(terse, path, false), paramsHashOf(long, path, false), "system shapes output by default")
	assert.Equal(t, paramsHashOf(terse, path, true), paramsHashOf(long, path, true), "exclude_system_prompt leaves it out")
}

func TestSemanticCacheMiddleware_ExcludeSystemPromptIgnoresTopLevelSystem(t *testing.T) {
	m, store, _ := newTestSemanticMiddleware(0.90, 10, true)
	terse := []byte(`{"model":"gpt-4","system":"be terse","messages":[{"role":"user","content":"what is 2+2"}]}`)
	thorough := []byte(`{"model":"gpt-4","system":"be thorough","messages":[{"role":"user","content":"what is 2+2"}]}`)

	serveSemanticRequest(t, m, terse, "")
	m.wg.Wait()
	require.Equal(t, 1, store.Len())

	rec := serveSemanticRequest(t, m, thorough, "")
	assert.Equal(t, "HIT (semantic)", rec.Header().Get("X-Cache"))
}

func TestComputeParamsHash_DuplicateMemberNamesHashVerbatim(t *testing.T) {
	const path = "/v1/chat/completions"
	duplicated := `{"model":"m","tool_choice":"none","tool_choice":"required","messages":[]}`
	deduplicated := `{"model":"m","tool_choice":"required","messages":[]}`
	assert.NotEqual(t, paramsHashOf(deduplicated, path, false), paramsHashOf(duplicated, path, false))
}

func TestSemanticCacheMiddleware_ParamsHashIsolation_ToolChoice(t *testing.T) {
	m, store, _ := newTestSemanticMiddleware(0.90, 10, false)
	auto := []byte(`{"model":"gpt-4","tool_choice":"auto","messages":[{"role":"user","content":"same prompt"}]}`)
	required := []byte(`{"model":"gpt-4","tool_choice":"required","messages":[{"role":"user","content":"same prompt"}]}`)

	serveSemanticRequest(t, m, auto, "")
	m.wg.Wait()
	require.Equal(t, 1, store.Len())

	rec := serveSemanticRequest(t, m, required, "")
	assert.NotEqual(t, "HIT (semantic)", rec.Header().Get("X-Cache"))

	rec = serveSemanticRequest(t, m, auto, "")
	assert.Equal(t, "HIT (semantic)", rec.Header().Get("X-Cache"))
}

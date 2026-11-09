package mistral

import (
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
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

func TestAdaptChatRequest(t *testing.T) {
	t.Run("nil passes through", func(t *testing.T) {
		got, err := adaptChatRequest(nil)
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("reasoning effort passes through lowercased", func(t *testing.T) {
		for _, effort := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "MAX"} {
			req := &core.ChatRequest{Model: "m", Reasoning: &core.Reasoning{Effort: effort}}
			got, err := adaptChatRequest(req)
			require.NoError(t, err)
			assert.Nil(t, got.Reasoning)
			exp := `"` + strings.ToLower(effort) + `"`
			assert.Equal(t, json.RawMessage(exp), got.ExtraFields.Lookup("reasoning_effort"), "effort %q", effort)
		}
	})

	t.Run("no reasoning leaves the request identical", func(t *testing.T) {
		req := &core.ChatRequest{Model: "m", Messages: []core.Message{{Role: "user", Content: "hi"}}}
		got, err := adaptChatRequest(req)
		require.NoError(t, err)
		assert.Same(t, req, got)
	})

	t.Run("reasoning without effort is dropped, not forwarded", func(t *testing.T) {
		req := &core.ChatRequest{Model: "m", Reasoning: &core.Reasoning{Summary: "auto"}}
		got, err := adaptChatRequest(req)
		require.NoError(t, err)
		assert.Nil(t, got.Reasoning, "Mistral rejects the unknown nested reasoning member with a 422")
		assert.Nil(t, got.ExtraFields.Lookup("reasoning_effort"))
	})
}

func TestAdaptMistralChatBody(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "replayed reasoning becomes a thinking chunk before the text",
			in:   `{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello","reasoning_content":"earlier thoughts"}]}`,
			want: `{"messages":[{"content":"hi","role":"user"},{"content":[{"thinking":[{"text":"earlier thoughts","type":"text"}],"type":"thinking"},{"text":"hello","type":"text"}],"role":"assistant"}],"model":"m"}`,
		},
		{
			name: "replayed array content keeps its parts and the reasoning goes first",
			in:   `{"messages":[{"role":"assistant","content":[{"type":"text","text":"a"}],"reasoning_content":"b","mistral_content_chunks":[{"type":"reference"}]}]}`,
			want: `{"messages":[{"content":[{"thinking":[{"text":"b","type":"text"}],"type":"thinking"},{"text":"a","type":"text"}],"role":"assistant"}]}`,
		},
		{
			name: "assistant without replay members returns nil",
			in:   `{"messages":[{"role":"assistant","content":"hello"}]}`,
			want: ``,
		},
		{
			name: "non-assistant reasoning_content member is left alone",
			in:   `{"messages":[{"role":"user","content":"hi","reasoning_content":"odd but not ours"}]}`,
			want: ``,
		},
		{
			name: "known members are renamed for Mistral's schema",
			in:   `{"model":"m","stream":true,"stream_options":{"include_usage":true},"max_completion_tokens":900,"seed":42,"messages":[{"role":"user","content":"hi"}]}`,
			want: `{"messages":[{"content":"hi","role":"user"}],"model":"m","random_seed":42,"stream":true,"max_tokens":900}`,
		},
		{
			name: "max_tokens wins over max_completion_tokens",
			in:   `{"max_tokens":7,"max_completion_tokens":900,"messages":[]}`,
			want: `{"messages":[],"max_tokens":7}`,
		},
		{
			name: "service_tier default asks for standard capacity, not auto",
			in:   `{"service_tier":"default","messages":[]}`,
			want: `{"messages":[],"service_tier":"standard_only"}`,
		},
		{
			name: "service_tier without a Mistral equivalent is dropped",
			in:   `{"service_tier":"priority","messages":[]}`,
			want: `{"messages":[]}`,
		},
		{
			name: "every replayed assistant message is adapted",
			in:   `{"messages":[{"role":"assistant","content":"one","reasoning_content":"r1"},{"role":"user","content":"between"},{"role":"assistant","content":"two","reasoning_content":"r2"}]}`,
			want: `{"messages":[{"role":"assistant","content":[{"thinking":[{"text":"r1","type":"text"}],"type":"thinking"},{"text":"one","type":"text"}]},{"content":"between","role":"user"},{"content":[{"thinking":[{"text":"r2","type":"text"}],"type":"thinking"},{"text":"two","type":"text"}],"role":"assistant"}]}`,
		},
		{
			name: "empty reasoning_content member is removed without a chunk",
			in:   `{"messages":[{"role":"assistant","content":"hello","reasoning_content":""}]}`,
			want: `{"messages":[{"content":"hello","role":"assistant"}]}`,
		},
		{
			name: "null reasoning_content member is removed",
			in:   `{"messages":[{"role":"assistant","content":"hello","reasoning_content":null}]}`,
			want: `{"messages":[{"content":"hello","role":"assistant"}]}`,
		},
		{
			name: "no messages member returns nil",
			in:   `{"model":"mistral-embed","input":["a"]}`,
			want: ``,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := adaptMistralChatBody([]byte(tt.in))
			if tt.want == "" {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.JSONEq(t, tt.want, string(got))
		})
	}
}

func TestAdaptChatWire(t *testing.T) {
	t.Run("nil body passes through", func(t *testing.T) {
		r := &llmclient.Request{Method: "GET", Endpoint: "/models"}
		adaptChatWire(r)
		assert.Nil(t, r.Body)
	})

	t.Run("chat body is patched in place as raw JSON", func(t *testing.T) {
		req := &core.ChatRequest{
			Model: "m",
			Messages: []core.Message{
				{Role: "assistant", Content: "hello", ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					reasoningContentKey: json.RawMessage(`"earlier thoughts"`),
				})},
			},
		}
		r := &llmclient.Request{Method: "POST", Endpoint: "/chat/completions", Body: req}
		adaptChatWire(r)
		raw, ok := r.Body.(json.RawMessage)
		require.True(t, ok, "body = %#v, want json.RawMessage", r.Body)
		var body map[string]any
		require.NoError(t, json.Unmarshal(raw, &body))
		messages := body["messages"].([]any)
		assistant := messages[0].(map[string]any)
		assert.NotContains(t, assistant, reasoningContentKey)
		content := assistant["content"].([]any)
		assert.JSONEq(t, `{"type":"thinking","thinking":[{"type":"text","text":"earlier thoughts"}]}`, string(mustJSON(content[0])))
		assert.JSONEq(t, `{"type":"text","text":"hello"}`, string(mustJSON(content[1])))
	})

	t.Run("body without patches keeps the typed marshal", func(t *testing.T) {
		req := &core.ChatRequest{Model: "m", Messages: []core.Message{{Role: "user", Content: "hi"}}}
		r := &llmclient.Request{Method: "POST", Endpoint: "/chat/completions", Body: req}
		adaptChatWire(r)
		assert.Same(t, req, r.Body, "unchanged bodies must not be re-encoded")
	})
}

func mustJSON(v any) []byte {
	encoded, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return encoded
}

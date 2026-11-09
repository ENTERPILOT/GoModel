package mistral

import (
	"net/http"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

func extras(t *testing.T, raw string) core.UnknownJSONFields {
	t.Helper()
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(raw), &fields))
	return core.UnknownJSONFieldsFromMap(fields)
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

	t.Run("plain request is returned as is", func(t *testing.T) {
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

	t.Run("OpenAI-only top-level members are adapted", func(t *testing.T) {
		req := &core.ChatRequest{
			Model:         "m",
			Stream:        true,
			StreamOptions: &core.StreamOptions{IncludeUsage: true},
			User:          "user-123",
			ServiceTier:   "default",
			ExtraFields:   extras(t, `{"max_completion_tokens":900,"seed":42,"logit_bias":{"1":2},"store":true,"verbosity":"low","safety_identifier":"s1","safe_prompt":true}`),
		}
		got, err := adaptChatRequest(req)
		require.NoError(t, err)
		require.NotSame(t, req, got)
		assert.Nil(t, got.StreamOptions, "usage arrives in Mistral's final stream chunk anyway")
		assert.Empty(t, got.User)
		assert.Equal(t, "standard_only", got.ServiceTier)
		assert.Equal(t, new(900), got.MaxTokens)
		assert.Equal(t, json.RawMessage(`42`), got.ExtraFields.Lookup("random_seed"))
		assert.Equal(t, json.RawMessage(`true`), got.ExtraFields.Lookup("safe_prompt"), "Mistral-native members stay")
		for _, key := range []string{"max_completion_tokens", "seed", "logit_bias", "store", "verbosity", "safety_identifier"} {
			assert.Nil(t, got.ExtraFields.Lookup(key), key)
		}
		assert.NotNil(t, req.StreamOptions, "the caller's request must not change")
	})

	t.Run("max_completion_tokens null means no limit, a non-integer is rejected", func(t *testing.T) {
		got, err := adaptChatRequest(&core.ChatRequest{Model: "m", ExtraFields: extras(t, `{"max_completion_tokens":null}`)})
		require.NoError(t, err)
		assert.Nil(t, got.MaxTokens, "null must not become max_tokens: 0")
		assert.Nil(t, got.ExtraFields.Lookup("max_completion_tokens"))

		for _, invalid := range []string{`"lots"`, `900.5`} {
			_, err = adaptChatRequest(&core.ChatRequest{Model: "m", ExtraFields: extras(t, `{"max_completion_tokens":`+invalid+`}`)})
			var gwErr *core.GatewayError
			require.ErrorAs(t, err, &gwErr, invalid)
			assert.Equal(t, http.StatusBadRequest, gwErr.HTTPStatusCode(), invalid)
		}
	})

	t.Run("Mistral spellings win over the OpenAI ones", func(t *testing.T) {
		req := &core.ChatRequest{
			Model:       "m",
			MaxTokens:   new(7),
			ExtraFields: extras(t, `{"max_completion_tokens":900,"seed":42,"random_seed":1}`),
		}
		got, err := adaptChatRequest(req)
		require.NoError(t, err)
		assert.Equal(t, new(7), got.MaxTokens)
		assert.Equal(t, json.RawMessage(`1`), got.ExtraFields.Lookup("random_seed"))
		assert.Nil(t, got.ExtraFields.Lookup("seed"))
	})

	t.Run("service tiers map to Mistral's enum", func(t *testing.T) {
		for tier, want := range map[string]string{"": "", "auto": "auto", "standard_only": "standard_only", "default": "standard_only", "priority": "", "flex": ""} {
			assert.Equal(t, want, mistralServiceTier(tier), tier)
		}
	})

	t.Run("messages keep only the members Mistral allows for their role", func(t *testing.T) {
		req := &core.ChatRequest{Model: "m", Messages: []core.Message{
			{Role: "system", Content: "be brief", ExtraFields: extras(t, `{"name":"ops"}`)},
			{Role: "user", Content: "hi", ExtraFields: extras(t, `{"name":"alice"}`)},
			{Role: "assistant", Content: "hello", ExtraFields: extras(t, `{"refusal":null,"annotations":[],"name":"bot","prefix":true,"reasoning_content":"r","mistral_content_chunks":[{"type":"reference"}]}`)},
			{Role: "tool", ToolCallID: "c1", Content: "42", ExtraFields: extras(t, `{"name":"calc","extra_content":{}}`)},
		}}
		got, err := adaptChatRequest(req)
		require.NoError(t, err)
		require.Len(t, got.Messages, 4)
		assert.True(t, got.Messages[0].ExtraFields.IsEmpty())
		assert.True(t, got.Messages[1].ExtraFields.IsEmpty())
		assistant := got.Messages[2].ExtraFields
		assert.Equal(t, json.RawMessage(`true`), assistant.Lookup("prefix"))
		assert.Equal(t, json.RawMessage(`"r"`), assistant.Lookup(reasoningContentKey), "kept for adaptChatWire")
		assert.False(t, assistant.HasAny("refusal", "annotations", "name", unknownChunksKey))
		tool := got.Messages[3].ExtraFields
		assert.Equal(t, json.RawMessage(`"calc"`), tool.Lookup("name"))
		assert.Nil(t, tool.Lookup("extra_content"))
		assert.NotNil(t, req.Messages[1].ExtraFields.Lookup("name"), "the caller's messages must not change")
	})

	t.Run("replayed reasoning is canonicalized", func(t *testing.T) {
		req := &core.ChatRequest{Model: "m", Messages: []core.Message{
			{Role: "assistant", Content: "a", ExtraFields: extras(t, `{"reasoning":"vendor spelling"}`)},
			{Role: "assistant", Content: "b", ExtraFields: extras(t, `{"reasoning_content":""}`)},
			{Role: "assistant", Content: "c", ExtraFields: extras(t, `{"reasoning_content":null}`)},
			{Role: "assistant", Content: "d", ExtraFields: extras(t, `{"reasoning_content":"","reasoning":"alias"}`)},
			{Role: "assistant", Content: "e", ExtraFields: extras(t, `{"reasoning_content":"primary","reasoning":"alias"}`)},
		}}
		got, err := adaptChatRequest(req)
		require.NoError(t, err)
		assert.Equal(t, json.RawMessage(`"vendor spelling"`), got.Messages[0].ExtraFields.Lookup(reasoningContentKey))
		assert.Nil(t, got.Messages[0].ExtraFields.Lookup("reasoning"))
		assert.True(t, got.Messages[1].ExtraFields.IsEmpty())
		assert.True(t, got.Messages[2].ExtraFields.IsEmpty())
		assert.Equal(t, json.RawMessage(`"alias"`), got.Messages[3].ExtraFields.Lookup(reasoningContentKey), "an empty primary falls back to the alias")
		assert.Equal(t, json.RawMessage(`"primary"`), got.Messages[4].ExtraFields.Lookup(reasoningContentKey), "a non-empty primary wins")
	})
}

func TestReplayReasoningAsThinking(t *testing.T) {
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
			in:   `{"messages":[{"role":"assistant","content":[{"type":"text","text":"a"}],"reasoning_content":"b"}]}`,
			want: `{"messages":[{"content":[{"thinking":[{"text":"b","type":"text"}],"type":"thinking"},{"text":"a","type":"text"}],"role":"assistant"}]}`,
		},
		{
			name: "tool-call turn without text gets the thinking chunk alone",
			in:   `{"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"c1"}],"reasoning_content":"plan"}]}`,
			want: `{"messages":[{"content":[{"thinking":[{"text":"plan","type":"text"}],"type":"thinking"}],"role":"assistant","tool_calls":[{"id":"c1"}]}]}`,
		},
		{
			name: "every replayed assistant message is adapted",
			in:   `{"messages":[{"role":"assistant","content":"one","reasoning_content":"r1"},{"role":"user","content":"between"},{"role":"assistant","content":"two","reasoning_content":"r2"}]}`,
			want: `{"messages":[{"role":"assistant","content":[{"thinking":[{"text":"r1","type":"text"}],"type":"thinking"},{"text":"one","type":"text"}]},{"content":"between","role":"user"},{"content":[{"thinking":[{"text":"r2","type":"text"}],"type":"thinking"},{"text":"two","type":"text"}],"role":"assistant"}]}`,
		},
		{
			name: "assistant without reasoning returns nil",
			in:   `{"messages":[{"role":"assistant","content":"hello"}]}`,
		},
		{
			name: "no messages member returns nil",
			in:   `{"model":"mistral-embed","input":["a"]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := replayReasoningAsThinking([]byte(tt.in))
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
	t.Run("non-chat bodies pass through", func(t *testing.T) {
		r := &llmclient.Request{Method: "GET", Endpoint: "/models"}
		adaptChatWire(r)
		assert.Nil(t, r.Body)

		embed := &core.EmbeddingRequest{Model: "mistral-embed"}
		r = &llmclient.Request{Method: "POST", Endpoint: "/embeddings", Body: embed}
		adaptChatWire(r)
		assert.Same(t, embed, r.Body)
	})

	t.Run("replayed reasoning is patched in as raw JSON", func(t *testing.T) {
		req := &core.ChatRequest{
			Model:    "m",
			Messages: []core.Message{{Role: "assistant", Content: "hello", ExtraFields: extras(t, `{"reasoning_content":"earlier thoughts"}`)}},
		}
		r := &llmclient.Request{Method: "POST", Endpoint: "/chat/completions", Body: req}
		adaptChatWire(r)
		raw, ok := r.Body.(json.RawMessage)
		require.True(t, ok, "body = %#v, want json.RawMessage", r.Body)
		assert.JSONEq(t, `{"model":"m","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"earlier thoughts"}]},{"type":"text","text":"hello"}]}]}`, string(raw))
	})

	t.Run("chat body without replayed reasoning is not re-encoded", func(t *testing.T) {
		req := &core.ChatRequest{Model: "m", Messages: []core.Message{{Role: "user", Content: "if a < b && c > d {}"}}}
		r := &llmclient.Request{Method: "POST", Endpoint: "/chat/completions", Body: req}
		adaptChatWire(r)
		assert.Same(t, req, r.Body)
	})
}

func mustJSON(v any) []byte {
	encoded, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return encoded
}

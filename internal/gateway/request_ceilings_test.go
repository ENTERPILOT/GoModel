package gateway

import (
	"context"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/requestparams"
)

func extras(fields map[string]string) core.UnknownJSONFields {
	raw := make(map[string]json.RawMessage, len(fields))
	for key, value := range fields {
		raw[key] = json.RawMessage(value)
	}
	return core.UnknownJSONFieldsFromMap(raw)
}

// rawFields decodes a raw extra fields object as the request decoder keeps
// it, repeated members included.
func rawFields(t *testing.T, raw string) core.UnknownJSONFields {
	t.Helper()
	var req core.ChatRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"m","messages":[],`+raw[1:]), &req))
	return req.ExtraFields
}

// A ceiling lowers what the client asked for and never raises it: a request
// at or below the ceiling, or one that sets no effort, is sent as it came.
func TestChatRequestWithCeilingsReasoningEffort(t *testing.T) {
	tests := []struct {
		name      string
		reasoning *core.Reasoning
		extras    map[string]string
		// raw, when set, is the extra fields object as sent, for bodies a
		// map cannot hold (a repeated member).
		raw     string
		ceiling string
		// want is the effort sent upstream; "" with unchanged means the
		// request is passed through untouched.
		want      string
		unchanged bool
	}{
		{name: "no effort sent is not raised to the ceiling", ceiling: "medium", unchanged: true},
		{name: "lower effort is kept", reasoning: &core.Reasoning{Effort: "low"}, ceiling: "medium", unchanged: true},
		{name: "effort equal to the ceiling is kept", reasoning: &core.Reasoning{Effort: "medium"}, ceiling: "medium", unchanged: true},
		{name: "higher effort is lowered", reasoning: &core.Reasoning{Effort: "high"}, ceiling: "medium", want: "medium"},
		{name: "max is lowered", reasoning: &core.Reasoning{Effort: "max"}, ceiling: "medium", want: "medium"},
		{name: "client spelling is normalized before ranking", reasoning: &core.Reasoning{Effort: " HIGH "}, ceiling: "medium", want: "medium"},
		{name: "none ceiling turns requested reasoning off", reasoning: &core.Reasoning{Effort: "minimal"}, ceiling: "none", want: "none"},
		{name: "none ceiling leaves a silent request alone", ceiling: "none", unchanged: true},
		{name: "flat reasoning_effort above the ceiling is lowered", extras: map[string]string{"reasoning_effort": `"xhigh"`}, ceiling: "medium", want: "medium"},
		{name: "flat reasoning_effort within the ceiling is kept", extras: map[string]string{"reasoning_effort": `"low"`}, ceiling: "medium", unchanged: true},
		{name: "the higher of two spellings decides", reasoning: &core.Reasoning{Effort: "low"}, extras: map[string]string{"reasoning_effort": `"high"`}, ceiling: "medium", want: "medium"},
		{name: "an effort outside the scale cannot pass the ceiling", reasoning: &core.Reasoning{Effort: "ultra"}, ceiling: "medium", want: "medium"},
		{name: "a non-string flat effort cannot pass the ceiling", extras: map[string]string{"reasoning_effort": `9`}, ceiling: "medium", want: "medium"},
		{name: "gemini thinking level above the ceiling is lowered", extras: map[string]string{"extra_body": `{"google":{"thinking_config":{"thinking_level":"HIGH"}}}`}, ceiling: "medium", want: "medium"},
		{name: "gemini thinking level within the ceiling is kept", extras: map[string]string{"extra_body": `{"google":{"thinking_config":{"thinkingLevel":"low"}}}`}, ceiling: "medium", unchanged: true},
		{name: "gemini thinking budget cannot be ranked, so the ceiling applies", extras: map[string]string{"extra_body": `{"google":{"thinking_config":{"thinking_budget":24576}}}`}, ceiling: "medium", want: "medium"},
		{name: "gemini zero budget is reasoning off and is kept", extras: map[string]string{"extra_body": `{"google":{"thinking_config":{"thinking_budget":0}}}`}, ceiling: "medium", unchanged: true},
		{name: "gemini object names match case-insensitively, as Gemini decodes them", extras: map[string]string{"extra_body": `{"Google":{"Thinking_Config":{"thinking_budget":32768}}}`}, ceiling: "low", want: "low"},
		{name: "gemini member names match in any spelling", extras: map[string]string{"extra_body": `{"google":{"thinking_config":{"Thinking_Level":"high"}}}`}, ceiling: "low", want: "low"},
		{name: "a repeated flat effort is checked in every occurrence", extras: map[string]string{"x": `0`}, raw: `{"reasoning_effort":"low","reasoning_effort":"max"}`, ceiling: "low", want: "low"},
		{name: "a thinking budget cannot be ranked", extras: map[string]string{"thinking": `{"type":"enabled","token_budget":31999}`}, ceiling: "none", want: "none"},
		{name: "disabled thinking is reasoning off", extras: map[string]string{"thinking": `{"type":"disabled"}`}, ceiling: "none", unchanged: true},
		{name: "enable_thinking true cannot be ranked", extras: map[string]string{"enable_thinking": `true`}, ceiling: "high", want: "high"},
		{name: "enable_thinking false is reasoning off", extras: map[string]string{"enable_thinking": `false`}, ceiling: "none", unchanged: true},
		{name: "a zero thinking_budget is reasoning off", extras: map[string]string{"thinking_budget": `0`}, ceiling: "none", unchanged: true},
		{name: "a thinking_budget cannot be ranked", extras: map[string]string{"thinking_budget": `100`}, ceiling: "medium", want: "medium"},
		{name: "ollama think level above the ceiling", extras: map[string]string{"think": `"high"`}, ceiling: "low", want: "low"},
		{name: "ollama think level within the ceiling", extras: map[string]string{"think": `"low"`}, ceiling: "low", unchanged: true},
		{name: "chat template reasoning switch", extras: map[string]string{"chat_template_kwargs": `{"enable_thinking":true}`}, ceiling: "low", want: "low"},
		{name: "chat template effort within the ceiling", extras: map[string]string{"chat_template_kwargs": `{"reasoning_effort":"low","enable_thinking":false}`}, ceiling: "low", unchanged: true},
		{name: "controls inside extra_body count too", extras: map[string]string{"extra_body": `{"enable_thinking":true}`}, ceiling: "low", want: "low"},
		{name: "no ceiling keeps any effort", reasoning: &core.Reasoning{Effort: "max"}, unchanged: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields := extras(tt.extras)
			if tt.raw != "" {
				fields = rawFields(t, tt.raw)
			}
			req := &core.ChatRequest{Model: "m", Reasoning: tt.reasoning, ExtraFields: fields}

			capped, err := ChatRequestWithCeilings(req, requestparams.Ceilings{ReasoningEffort: tt.ceiling})
			require.NoError(t, err)

			assert.Equal(t, !tt.unchanged, ChatRequestExceedsCeilings(req, requestparams.Ceilings{ReasoningEffort: tt.ceiling}))
			if tt.unchanged {
				assert.Same(t, req, capped)
				return
			}
			require.NotNil(t, capped.Reasoning)
			assert.Equal(t, tt.want, capped.Reasoning.Effort)
			assert.Nil(t, capped.ExtraFields.Lookup("reasoning_effort"), "no other spelling may outbid the ceiling")
			assert.Nil(t, capped.ExtraFields.Lookup("extra_body"), "no other spelling may outbid the ceiling")
			for _, field := range append([]string{chatTemplateKwargsField}, reasoningControlFields...) {
				assert.Nil(t, capped.ExtraFields.Lookup(field), "no other spelling may outbid the ceiling")
			}
			assert.Equal(t, tt.reasoning, req.Reasoning, "the client's request must not be mutated")
		})
	}
}

func TestResponsesRequestWithCeilings(t *testing.T) {
	ceilings := requestparams.Ceilings{ReasoningEffort: "medium", OutputTokens: 100}

	silent := &core.ResponsesRequest{Model: "m"}
	capped, err := ResponsesRequestWithCeilings(silent, ceilings)
	require.NoError(t, err)
	assert.Nil(t, capped.Reasoning, "an omitted effort stays omitted")
	require.NotNil(t, capped.MaxOutputTokens, "an omitted token limit is set to the ceiling")
	assert.Equal(t, 100, *capped.MaxOutputTokens)
	assert.Nil(t, silent.MaxOutputTokens, "the client's request must not be mutated")

	within := &core.ResponsesRequest{Model: "m", Reasoning: &core.Reasoning{Effort: "low"}, MaxOutputTokens: new(64)}
	capped, err = ResponsesRequestWithCeilings(within, ceilings)
	require.NoError(t, err)
	assert.Same(t, within, capped)

	above := &core.ResponsesRequest{Model: "m", Reasoning: &core.Reasoning{Effort: "xhigh", Summary: "detailed"}, MaxOutputTokens: new(9000)}
	capped, err = ResponsesRequestWithCeilings(above, ceilings)
	require.NoError(t, err)
	require.NotNil(t, capped.Reasoning)
	assert.Equal(t, "medium", capped.Reasoning.Effort)
	assert.Equal(t, "detailed", capped.Reasoning.Summary, "the client's summary request survives")
	require.NotNil(t, capped.MaxOutputTokens)
	assert.Equal(t, 100, *capped.MaxOutputTokens)
	assert.Equal(t, "xhigh", above.Reasoning.Effort, "the client's request must not be mutated")
	assert.Equal(t, 9000, *above.MaxOutputTokens)
}

// Ceilings go last: they bound what a virtual model forces or defaults just
// as they bound the client's own value. Only the token ceiling fills in a
// value nobody set.
func TestRequestOverridesComposeWithCeilings(t *testing.T) {
	set := func(key, value string) requestparams.Set {
		return requestparams.Set{key: json.RawMessage(value)}
	}
	tests := []struct {
		name       string
		client     *core.ChatRequest
		overrides  requestparams.Overrides
		wantEffort string
		wantTokens *int
	}{
		{
			name:       "forced effort above the ceiling is lowered to it",
			client:     &core.ChatRequest{Model: "m", Reasoning: &core.Reasoning{Effort: "low"}},
			overrides:  requestparams.Overrides{ReasoningEffort: "high", Ceilings: requestparams.Ceilings{ReasoningEffort: "medium"}},
			wantEffort: "medium",
		},
		{
			name:       "forced effort below the ceiling stays forced",
			client:     &core.ChatRequest{Model: "m", Reasoning: &core.Reasoning{Effort: "max"}},
			overrides:  requestparams.Overrides{ReasoningEffort: "low", Ceilings: requestparams.Ceilings{ReasoningEffort: "medium"}},
			wantEffort: "low",
		},
		{
			name:       "forced effort applies to a silent client, within the ceiling",
			client:     &core.ChatRequest{Model: "m"},
			overrides:  requestparams.Overrides{ReasoningEffort: "xhigh", Ceilings: requestparams.Ceilings{ReasoningEffort: "medium"}},
			wantEffort: "medium",
		},
		{
			name:       "default token limit above the ceiling is lowered to it",
			client:     &core.ChatRequest{Model: "m"},
			overrides:  requestparams.Overrides{Defaults: set("max_tokens", `8000`), Ceilings: requestparams.Ceilings{OutputTokens: 4096}},
			wantTokens: new(4096),
		},
		{
			name:       "default token limit below the ceiling is kept",
			client:     &core.ChatRequest{Model: "m"},
			overrides:  requestparams.Overrides{Defaults: set("max_tokens", `1000`), Ceilings: requestparams.Ceilings{OutputTokens: 4096}},
			wantTokens: new(1000),
		},
		{
			name:       "client value beats the default and is then capped",
			client:     &core.ChatRequest{Model: "m", MaxTokens: new(6000)},
			overrides:  requestparams.Overrides{Defaults: set("max_tokens", `1000`), Ceilings: requestparams.Ceilings{OutputTokens: 4096}},
			wantTokens: new(4096),
		},
		{
			name:       "forced token limit above the ceiling is lowered to it",
			client:     &core.ChatRequest{Model: "m", MaxTokens: new(10)},
			overrides:  requestparams.Overrides{Params: set("max_tokens", `9000`), Ceilings: requestparams.Ceilings{OutputTokens: 4096}},
			wantTokens: new(4096),
		},
		{
			name:       "no client value and no default gets the ceiling as its limit and no effort",
			client:     &core.ChatRequest{Model: "m"},
			overrides:  requestparams.Overrides{Ceilings: requestparams.Ceilings{ReasoningEffort: "medium", OutputTokens: 4096}},
			wantTokens: new(4096),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			applied, err := ChatRequestWithOverrides(tt.client, tt.overrides)
			require.NoError(t, err)

			if tt.wantEffort == "" {
				assert.Nil(t, applied.Reasoning)
			} else {
				require.NotNil(t, applied.Reasoning)
				assert.Equal(t, tt.wantEffort, applied.Reasoning.Effort)
			}
			assert.Equal(t, tt.wantTokens, applied.MaxTokens)
		})
	}
}

// Every attempt is bounded: the primary and the failover leg each get their
// own model's forced effort, lowered to the caller's ceiling, and the
// client's token limit capped.
func TestCeilingsApplyToEveryAttempt(t *testing.T) {
	ceilings := requestparams.Ceilings{ReasoningEffort: "low", OutputTokens: 100}

	t.Run("chat", func(t *testing.T) {
		orchestrator, provider, resolver, workflow := effortFixture()
		resolver.efforts = map[string]string{"openai/primary": "high", "openai/backup": "minimal"}
		resolver.ceilings = ceilings
		original := &core.ChatRequest{Model: "primary", Provider: "openai", Reasoning: &core.Reasoning{Effort: "max"}, MaxTokens: new(5000)}

		_, _, err := orchestrator.DispatchChatCompletion(context.Background(), workflow, original)
		require.NoError(t, err)

		require.Len(t, provider.chats, 2)
		for i, want := range []string{"low", "minimal"} {
			sent := provider.chats[i]
			require.NotNil(t, sent.Reasoning, "attempt %d", i)
			assert.Equal(t, want, sent.Reasoning.Effort, "attempt %d", i)
			require.NotNil(t, sent.MaxTokens, "attempt %d", i)
			assert.Equal(t, 100, *sent.MaxTokens, "attempt %d", i)
		}
		assert.Equal(t, "max", original.Reasoning.Effort, "client request must not be mutated")
		assert.Equal(t, 5000, *original.MaxTokens)
	})

	t.Run("chat without a virtual model override", func(t *testing.T) {
		orchestrator, provider, resolver, workflow := effortFixture()
		resolver.efforts = nil
		resolver.ceilings = ceilings
		original := &core.ChatRequest{Model: "primary", Provider: "openai", Reasoning: &core.Reasoning{Effort: "high"}}

		_, _, err := orchestrator.DispatchChatCompletion(context.Background(), workflow, original)
		require.NoError(t, err)

		require.Len(t, provider.chats, 2)
		for i, sent := range provider.chats {
			require.NotNil(t, sent.Reasoning, "attempt %d", i)
			assert.Equal(t, "low", sent.Reasoning.Effort, "attempt %d", i)
			require.NotNil(t, sent.MaxTokens, "attempt %d: an omitted limit is set to the ceiling", i)
			assert.Equal(t, 100, *sent.MaxTokens, "attempt %d", i)
		}
	})

	t.Run("prepared chat failover leg", func(t *testing.T) {
		orchestrator, provider, resolver, workflow := effortFixture()
		resolver.efforts = nil
		resolver.ceilings = ceilings
		prepared, err := orchestrator.PrepareChatRequest(context.Background(), &core.ChatRequest{
			Model: "primary", Provider: "openai", Reasoning: &core.Reasoning{Effort: "high"}, MaxTokens: new(5000),
		}, RequestMeta{Workflow: workflow})
		require.NoError(t, err)
		require.NotNil(t, prepared.Request.MaxTokens)
		assert.Equal(t, 100, *prepared.Request.MaxTokens, "the prepared request, which keys the response cache, is already capped")

		_, _, err = orchestrator.DispatchChatCompletion(prepared.Context, prepared.Workflow, prepared.Request)
		require.NoError(t, err)

		require.Len(t, provider.chats, 2)
		for i, sent := range provider.chats {
			assert.Equal(t, "low", sent.Reasoning.Effort, "attempt %d", i)
			assert.Equal(t, 100, *sent.MaxTokens, "attempt %d", i)
		}
	})

	t.Run("streamed chat", func(t *testing.T) {
		orchestrator, provider, resolver, workflow := effortFixture()
		resolver.efforts = nil
		resolver.ceilings = ceilings

		result, err := orchestrator.StreamChatCompletion(context.Background(), workflow, &core.ChatRequest{
			Model: "primary", Provider: "openai", Stream: true, Reasoning: &core.Reasoning{Effort: "high"}, MaxTokens: new(5000),
		})
		require.NoError(t, err)
		require.NoError(t, result.Stream.Close())

		require.Len(t, provider.chats, 2)
		for i, sent := range provider.chats {
			assert.Equal(t, "low", sent.Reasoning.Effort, "attempt %d", i)
			assert.Equal(t, 100, *sent.MaxTokens, "attempt %d", i)
		}
	})

	t.Run("responses", func(t *testing.T) {
		orchestrator, provider, resolver, workflow := effortFixture()
		resolver.efforts = map[string]string{"openai/primary": "high", "openai/backup": "minimal"}
		resolver.ceilings = ceilings

		_, _, err := orchestrator.DispatchResponses(context.Background(), workflow, &core.ResponsesRequest{
			Model: "primary", Provider: "openai", Input: "hi", Reasoning: &core.Reasoning{Effort: "max"}, MaxOutputTokens: new(5000),
		})
		require.NoError(t, err)

		require.Len(t, provider.responses, 2)
		for i, want := range []string{"low", "minimal"} {
			sent := provider.responses[i]
			require.NotNil(t, sent.Reasoning, "attempt %d", i)
			assert.Equal(t, want, sent.Reasoning.Effort, "attempt %d", i)
			require.NotNil(t, sent.MaxOutputTokens, "attempt %d", i)
			assert.Equal(t, 100, *sent.MaxOutputTokens, "attempt %d", i)
		}
	})
}

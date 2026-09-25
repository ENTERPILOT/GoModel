package minimax

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// Verbatim buffered response from a live MiniMax OpenAI-compatible capture
// (reasoning_split: true). reasoning_content and reasoning_details arrive
// natively; content is clean.
const capturedChatCompletionJSON = `{"id":"07008f32b85f686000437e3973488aaf","object":"chat.completion","model":"MiniMax-M2.7","provider":"minimax","choices":[{"message":{"role":"assistant","content":"**Proof.** Let odd numbers be ` + "`2a + 1`" + ` and ` + "`2b + 1`" + `. Their sum: ` + "`(2a + 1) + (2b + 1) = 2a + 2b + 2 = 2(a + b + 1)`" + `. Divisible by 2. Even. ∎","name":"MiniMax AI","audio_content":"","reasoning_content":"The user wants a proof that the sum of two odd numbers is even. I'll keep this in caveman style as per the instructions.","reasoning_details":[{"type":"reasoning.text","id":"reasoning-text-1","format":"MiniMax-response-v1","index":0,"text":"The user wants a proof that the sum of two odd numbers is even. I'll keep this in caveman style as per the instructions."}]},"finish_reason":"stop","index":0}],"usage":{"completion_tokens":103,"prompt_tokens":769,"total_characters":0,"total_tokens":872},"created":1790008370,"input_sensitive":false,"output_sensitive":false,"input_sensitive_type":0,"output_sensitive_type":0,"output_sensitive_int":0,"base_resp":{"status_code":0,"status_msg":""}}`

func TestIsReasoningModel(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  bool
	}{
		{name: "m2", model: "minimax-m2", want: true},
		{name: "m2 is case-insensitive", model: "MiniMax-M2", want: true},
		{name: "m2.5", model: "MINIMAX-M2.5", want: true},
		{name: "m2.7", model: "minimax-m2.7", want: true},
		{name: "m3", model: "MiniMax-M3", want: true},
		{name: "m2.5 highspeed", model: "MiniMax-M2.5-HighSpeed", want: true},
		{name: "m2.7 highspeed", model: "minimax-m2.7-highspeed", want: true},
		{name: "m3.1 is not on the list", model: "minimax-m3.1"},
		{name: "m4 is not on the list", model: "minimax-m4"},
		{name: "m2 highspeed is not on the list", model: "minimax-m2-highspeed"},
		{name: "plain m2.1 is not on the list", model: "minimax-m2.1"},
		{name: "m2.1 highspeed is not on the list", model: "minimax-m2.1-highspeed"},
		{name: "other providers' models", model: "gpt-4o"},
		{name: "prefixes do not match", model: "minimax-m2-turbo"},
		{name: "empty model", model: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isReasoningModel(tt.model))
		})
	}
}

func TestAdaptChatRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     *core.ChatRequest
		want    *core.ChatRequest // nil means a new request carrying reasoning_split
		sameReq bool              // the original pointer must come back
	}{
		{
			name: "gated model gains reasoning_split",
			req:  &core.ChatRequest{Model: "MiniMax-M2.5"},
		},
		{
			name: "unknown members survive the merge",
			req: &core.ChatRequest{
				Model:       "minimax-m3",
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"x_vendor": json.RawMessage(`"v"`)}),
			},
		},
		{
			name:    "non-gated models come back unchanged",
			req:     &core.ChatRequest{Model: "minimax-text"},
			sameReq: true,
		},
		{
			name: "a caller-set flag is not overwritten",
			req: &core.ChatRequest{
				Model:       "minimax-m2",
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"reasoning_split": json.RawMessage(`false`)}),
			},
			sameReq: true,
		},
		{
			name:    "nil request comes back unchanged",
			req:     nil,
			sameReq: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := adaptChatRequest(tt.req)
			if tt.sameReq {
				require.Same(t, tt.req, got)
				return
			}
			require.NotSame(t, tt.req, got)
			assert.Equal(t, json.RawMessage(`true`), got.ExtraFields.Lookup(reasoningSplitKey))
			assert.Nil(t, tt.req.ExtraFields.Lookup(reasoningSplitKey), "original request must not be mutated")
			if tt.req.ExtraFields.Lookup("x_vendor") != nil {
				assert.Equal(t, json.RawMessage(`"v"`), got.ExtraFields.Lookup("x_vendor"))
			}
		})
	}
}

func TestAdaptChatRequest_MergeFailureReturnsOriginal(t *testing.T) {
	req := &core.ChatRequest{
		Model: "minimax-m3",
		// UnknownJSONFieldsFromMap stores values verbatim, so this builds a
		// malformed container the merge must reject.
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"x_broken": json.RawMessage(`{`)}),
	}
	require.Same(t, req, adaptChatRequest(req))
}

func TestChatCompletion_SendsReasoningSplitPerModel(t *testing.T) {
	tests := []struct {
		name   string
		model  string
		caller string // caller-supplied reasoning_split, "" for none
		want   any    // nil means the field must be absent
	}{
		{name: "gated model splits reasoning", model: "MiniMax-M2.7", want: true},
		{name: "non-gated model is left alone", model: "minimax-text"},
		{name: "caller choice wins", model: "minimax-m3", caller: "false", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, capturedChatCompletionJSON)
			provider := newTestProvider("minimax-key", server.URL, server.Client(), llmclient.Hooks{})

			req := &core.ChatRequest{Model: tt.model, Messages: []core.Message{{Role: "user", Content: "hi"}}}
			if tt.caller != "" {
				req.ExtraFields = core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					"reasoning_split": json.RawMessage(tt.caller),
				})
			}
			resp, err := provider.ChatCompletion(context.Background(), req)
			require.NoError(t, err)
			require.Len(t, resp.Choices, 1)

			raw := capture.Last(t).JSON(t)
			if tt.want == nil {
				assert.NotContains(t, raw, "reasoning_split")
				return
			}
			assert.Equal(t, tt.want, raw["reasoning_split"])
		})
	}
}

func TestChatCompletion_RelaysNativeReasoningMembers(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, capturedChatCompletionJSON)
	provider := newTestProvider("minimax-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "MiniMax-M2.7",
		Messages: []core.Message{{Role: "user", Content: "prove it"}},
	})
	require.NoError(t, err)
	require.Len(t, resp.Choices, 1)

	msg := resp.Choices[0].Message
	assert.Equal(t, "**Proof.** Let odd numbers be `2a + 1` and `2b + 1`. Their sum: `(2a + 1) + (2b + 1) = 2a + 2b + 2 = 2(a + b + 1)`. Divisible by 2. Even. ∎", msg.Content, "content must stay clean and untouched")
	require.NotNil(t, msg.ExtraFields.Lookup("reasoning_content"), "reasoning_content must be relayed")
	assert.Equal(t, `"The user wants a proof that the sum of two odd numbers is even. I'll keep this in caveman style as per the instructions."`, string(msg.ExtraFields.Lookup("reasoning_content")))
	require.NotNil(t, msg.ExtraFields.Lookup(reasoningDetailsKey), "reasoning_details must be relayed untouched")
	assert.JSONEq(t,
		`[{"type":"reasoning.text","id":"reasoning-text-1","format":"MiniMax-response-v1","index":0,"text":"The user wants a proof that the sum of two odd numbers is even. I'll keep this in caveman style as per the instructions."}]`,
		string(msg.ExtraFields.Lookup(reasoningDetailsKey)))
}

func TestUpstreamErrorPathsPropagate(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusInternalServerError, `{"error":{"message":"boom"}}`)
	provider := newTestProvider("minimax-key", server.URL, server.Client(), llmclient.Hooks{})
	req := &core.ChatRequest{Model: "MiniMax-M2.5", Messages: []core.Message{{Role: "user", Content: "hi"}}}

	_, err := provider.ChatCompletion(context.Background(), req)
	require.Error(t, err, "buffered upstream error must propagate")

	_, err = provider.StreamChatCompletion(context.Background(), req)
	require.Error(t, err, "streaming upstream error must propagate")
}

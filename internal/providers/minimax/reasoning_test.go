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

const minimalChatCompletionJSON = `{"id":"c1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`

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
		{name: "gated model splits reasoning", model: "MiniMax-M2.5", want: true},
		{name: "non-gated model is left alone", model: "minimax-text"},
		{name: "caller choice wins", model: "minimax-m3", caller: "false", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, minimalChatCompletionJSON)
			provider := newTestProvider("minimax-key", server.URL, server.Client(), llmclient.Hooks{})

			req := &core.ChatRequest{Model: tt.model, Messages: []core.Message{{Role: "user", Content: "hi"}}}
			if tt.caller != "" {
				req.ExtraFields = core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					"reasoning_split": json.RawMessage(tt.caller),
				})
			}
			_, err := provider.ChatCompletion(context.Background(), req)
			require.NoError(t, err)

			raw := capture.Last(t).JSON(t)
			if tt.want == nil {
				assert.NotContains(t, raw, "reasoning_split")
				return
			}
			assert.Equal(t, tt.want, raw["reasoning_split"])
		})
	}
}

func TestNormalizeChatResponse(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    string
		absent  string
	}{
		{
			name:    "reasoning_details are joined into reasoning_content",
			message: `{"role":"assistant","content":"4","reasoning_details":[{"text":"2 + "},{"text":"2 = 4"}]}`,
			want:    `"reasoning_content":"2 + 2 = 4"`,
		},
		{
			name:    "existing reasoning_content wins",
			message: `{"role":"assistant","content":"4","reasoning_details":[{"text":"derived"}],"reasoning_content":"kept"}`,
			want:    `"reasoning_content":"kept"`,
			absent:  `2 + 2 = 4`,
		},
		{
			name:    "a message without reasoning_details is untouched",
			message: `{"role":"assistant","content":"4","channel":"final"}`,
			want:    `"channel":"final"`,
			absent:  `"reasoning_content"`,
		},
		{
			name:    "a reasoning_details value of the wrong shape is untouched",
			message: `{"role":"assistant","content":"4","reasoning_details":"nope"}`,
			want:    `"content":"4"`,
			absent:  `"reasoning_content"`,
		},
		{
			name:    "empty reasoning_details add nothing",
			message: `{"role":"assistant","content":"4","reasoning_details":[]}`,
			want:    `"content":"4"`,
			absent:  `"reasoning_content"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var msg core.ResponseMessage
			err := json.Unmarshal([]byte(tt.message), &msg)
			require.NoError(t, err)

			resp := &core.ChatResponse{Choices: []core.Choice{{Message: msg}}}
			normalizeChatResponse(resp)

			encoded, err := json.Marshal(resp.Choices[0].Message)
			require.NoError(t, err)
			assert.Contains(t, string(encoded), tt.want)
			if tt.absent != "" {
				assert.NotContains(t, string(encoded), tt.absent)
			}
		})
	}
}

func TestNormalizeChatResponse_MergeFailureLeavesMessageUntouched(t *testing.T) {
	msg := &core.ResponseMessage{ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
		"reasoning_details": json.RawMessage(`[{"text":"kept"}]`),
		"zz_broken":         json.RawMessage(`{`),
	})}
	normalizeReasoningMessage(msg)
	assert.Equal(t, json.RawMessage(`[{"text":"kept"}]`), msg.ExtraFields.Lookup(reasoningDetailsKey))
	assert.Nil(t, msg.ExtraFields.Lookup(canonicalReasoningKey))
}

func TestNormalizeChatResponseNilIsSafe(t *testing.T) {
	normalizeChatResponse(nil)
}

func TestChatCompletion_NormalizesReasoningDetailsForGatedModel(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"id":"c1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"4","reasoning_details":[{"text":"2 + "},{"text":"2 = 4"}]},"finish_reason":"stop"}]}`)

	tests := []struct {
		name          string
		model         string
		wantReasoning string // "" means reasoning_content must be absent
	}{
		{name: "gated model", model: "MiniMax-M2.5", wantReasoning: "2 + 2 = 4"},
		{name: "non-gated model", model: "minimax-text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := newTestProvider("minimax-key", server.URL, server.Client(), llmclient.Hooks{})
			resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model:    tt.model,
				Messages: []core.Message{{Role: "user", Content: "hi"}},
			})
			require.NoError(t, err)
			require.Len(t, resp.Choices, 1)

			got := resp.Choices[0].Message.ExtraFields.Lookup(canonicalReasoningKey)
			if tt.wantReasoning == "" {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, `"2 + 2 = 4"`, string(got), "reasoning_content must hold the joined text")
			assert.Equal(t, "4", resp.Choices[0].Message.Content, "content must stay clean")
		})
	}
}

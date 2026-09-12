package opencodego

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// chatServer serves a minimal chat completion and records the outgoing body.
func chatServer(t *testing.T) (*httptest.Server, *providertest.Capture) {
	t.Helper()
	return providertest.JSONServer(t, http.StatusOK, `{
		"id":"chatcmpl-opencode",
		"created":1677652288,
		"model":"ox-alpha-free",
		"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]
	}`)
}

func TestChatCompletion_InjectsDefaultReasoningEffortWhenAbsent(t *testing.T) {
	server, capture := chatServer(t)

	_, err := newTestProvider(server.URL, server.Client()).ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "ox-alpha-free",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	got := capture.Last(t).JSON(t)
	assert.Equal(t, "low", got["reasoning_effort"])
	assert.NotContains(t, got, "reasoning")
}

func TestChatCompletion_MapsExplicitReasoningEffort(t *testing.T) {
	tests := []struct {
		name   string
		effort string
		want   string
	}{
		{name: "low passes through", effort: "low", want: "low"},
		{name: "medium downgrades", effort: "medium", want: "low"},
		{name: "none becomes low", effort: "none", want: "low"},
		{name: "high passes through", effort: "high", want: "high"},
		{name: "xhigh maps to max", effort: "xhigh", want: "max"},
		{name: "max passes through", effort: "MAX", want: "max"},
		{name: "unknown level passes through", effort: "turbo", want: "turbo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := chatServer(t)

			_, err := newTestProvider(server.URL, server.Client()).ChatCompletion(context.Background(), &core.ChatRequest{
				Model:     "ox-alpha-free",
				Messages:  []core.Message{{Role: "user", Content: "hi"}},
				Reasoning: &core.Reasoning{Effort: tt.effort},
			})
			require.NoError(t, err)
			assert.Equal(t, tt.want, capture.Last(t).JSON(t)["reasoning_effort"])
		})
	}
}

func TestChatCompletion_KeepsClientSuppliedFlatReasoningEffort(t *testing.T) {
	server, capture := chatServer(t)

	extra, err := core.MergeUnknownJSONFields(core.UnknownJSONFields{}, map[string]json.RawMessage{
		"reasoning_effort": json.RawMessage(`"max"`),
	})
	require.NoError(t, err)
	_, err = newTestProvider(server.URL, server.Client()).ChatCompletion(context.Background(), &core.ChatRequest{
		Model:       "ox-alpha-free",
		Messages:    []core.Message{{Role: "user", Content: "hi"}},
		ExtraFields: extra,
	})
	require.NoError(t, err)
	assert.Equal(t, "max", capture.Last(t).JSON(t)["reasoning_effort"])
}

// TestChatCompletion_NestedReasoningWinsOverFlatField pins the precedence a
// self-contradicting request gets: reasoning.effort is GoModel's canonical
// field, so it wins over a flat reasoning_effort sent alongside it, matching
// every other provider built on AdaptReasoningEffortRequest. The flat field is
// only authoritative when no canonical reasoning is present, where it stops the
// default from being injected over it.
func TestChatCompletion_NestedReasoningWinsOverFlatField(t *testing.T) {
	server, capture := chatServer(t)

	extra, err := core.MergeUnknownJSONFields(core.UnknownJSONFields{}, map[string]json.RawMessage{
		"reasoning_effort": json.RawMessage(`"max"`),
	})
	require.NoError(t, err)
	_, err = newTestProvider(server.URL, server.Client()).ChatCompletion(context.Background(), &core.ChatRequest{
		Model:       "ox-alpha-free",
		Messages:    []core.Message{{Role: "user", Content: "hi"}},
		Reasoning:   &core.Reasoning{Effort: "medium"},
		ExtraFields: extra,
	})
	require.NoError(t, err)
	assert.Equal(t, "low", capture.Last(t).JSON(t)["reasoning_effort"])
}

func TestChatCompletion_DefaultReasoningEffortEnvOverride(t *testing.T) {
	t.Setenv(defaultReasoningEffortEnvVar, "high")

	server, capture := chatServer(t)
	_, err := newTestProvider(server.URL, server.Client()).ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "ox-alpha-free",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "high", capture.Last(t).JSON(t)["reasoning_effort"])
}

func TestChatCompletion_DefaultReasoningEffortDisabled(t *testing.T) {
	for _, value := range []string{"none", "OFF"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(defaultReasoningEffortEnvVar, value)

			server, capture := chatServer(t)
			_, err := newTestProvider(server.URL, server.Client()).ChatCompletion(context.Background(), &core.ChatRequest{
				Model:    "ox-alpha-free",
				Messages: []core.Message{{Role: "user", Content: "hi"}},
			})
			require.NoError(t, err)
			assert.NotContains(t, capture.Last(t).JSON(t), "reasoning_effort", "want absent when injection is disabled")
		})
	}
}

func TestStreamChatCompletion_InjectsDefaultReasoningEffort(t *testing.T) {
	server, capture := providertest.SSEServer(t, "data: [DONE]\n\n")

	body, err := newTestProvider(server.URL, server.Client()).StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "ox-alpha-free",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
		Stream:   true,
	})
	require.NoError(t, err)

	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()

	assert.Equal(t, "low", capture.Last(t).JSON(t)["reasoning_effort"])
}

// TestResponses_InjectsDefaultReasoningEffort covers the /v1/responses path:
// it is translated to a chat completion by ResponsesViaChat, so the adaptation
// must reach the upstream body there too.
func TestResponses_InjectsDefaultReasoningEffort(t *testing.T) {
	server, capture := chatServer(t)
	_, err := newTestProvider(server.URL, server.Client()).Responses(context.Background(), &core.ResponsesRequest{
		Model: "ox-alpha-free",
		Input: "hi",
	})
	require.NoError(t, err)
	assert.Equal(t, "low", capture.Last(t).JSON(t)["reasoning_effort"])
}

// TestChatCompletion_MessagesModelUnaffected pins that the injection lives on
// the /chat/completions path only: the Anthropic-native dialect has no
// reasoning_effort field.
func TestChatCompletion_MessagesModelUnaffected(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"id":"msg_opencode",
		"model":"qwen3.7-max",
		"content":[{"type":"text","text":"hello"}],
		"stop_reason":"end_turn",
		"usage":{"input_tokens":5,"output_tokens":2}
	}`)
	_, err := newTestProvider(server.URL, server.Client()).ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "qwen3.7-max",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.NotContains(t, capture.Last(t).JSON(t), "reasoning_effort", "want absent on the /messages dialect")
}

func TestAdaptChatRequest_NilRequest(t *testing.T) {
	req, err := adaptChatRequest(defaultReasoningEffort)(nil)
	require.NoError(t, err)
	require.Nil(t, req)
}

func TestLoadDefaultReasoningEffort(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string
	}{
		{name: "unset uses default", env: "", want: "low"},
		{name: "override normalized", env: " XHIGH ", want: "max"},
		{name: "none disables", env: "none", want: ""},
		{name: "off disables", env: "off", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(defaultReasoningEffortEnvVar, tt.env)
			assert.Equal(t, tt.want, loadDefaultReasoningEffort())
		})
	}
}

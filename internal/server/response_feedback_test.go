package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
)

type capturedResponseFeedback struct {
	requestID     string
	endpoint      ext.Endpoint
	sessionID     string
	model         string
	providerType  string
	providerName  string
	inputTokens   int
	cacheRead     int
	cacheWrite    int
	usageObserved bool
}

type feedbackCaptureObserver struct {
	feedback []capturedResponseFeedback
}

func (o *feedbackCaptureObserver) ObserveResponse(
	_ context.Context,
	requestID string,
	endpoint ext.Endpoint,
	sessionID, model, providerType, providerName string,
	inputTokens, cachedInputTokens, cacheWriteInputTokens int,
	usageObserved bool,
) {
	o.feedback = append(o.feedback, capturedResponseFeedback{
		requestID: requestID, endpoint: endpoint, sessionID: sessionID,
		model: model, providerType: providerType, providerName: providerName,
		inputTokens: inputTokens, cacheRead: cachedInputTokens, cacheWrite: cacheWriteInputTokens,
		usageObserved: usageObserved,
	})
}

func TestNotifyChatResponseFeedbackIncludesRouteAndCacheUsage(t *testing.T) {
	observer := &feedbackCaptureObserver{}
	ctx := core.WithRequestID(context.Background(), "req-1")
	ctx = core.WithSessionID(ctx, "session-1")
	resp := &core.ChatResponse{
		Model: "gpt-5.6",
		Usage: core.Usage{
			PromptTokens:        2400,
			PromptTokensDetails: &core.PromptTokensDetails{CachedTokens: 1800},
			RawUsage:            map[string]any{"cache_creation_input_tokens": 300},
		},
	}

	usage := cacheUsageFromCore(resp.Usage.PromptTokens, resp.Usage.PromptTokensDetails, resp.Usage.RawUsage)
	notifyResponseFeedback(ctx, []ext.ResponseFeedbackObserver{observer}, "req-1", "session-1", ext.EndpointChatCompletions, resp.Model, "anthropic", "primary", usage)
	require.Len(t, observer.feedback, 1)

	got := observer.feedback[0]
	require.Equal(t, "req-1", got.requestID)
	require.Equal(t, "session-1", got.sessionID)
	require.Equal(t, "gpt-5.6", got.model)
	require.Equal(t, "anthropic", got.providerType)
	require.Equal(t, "primary", got.providerName)
	require.Equal(t, 2400, got.inputTokens)
	require.Equal(t, 1800, got.cacheRead)
	require.Equal(t, 300, got.cacheWrite)
	require.True(t, got.usageObserved, "feedback = %+v", got)
}

func TestNotifyResponsesResponseFeedbackPreservesObservedZeroUsage(t *testing.T) {
	observer := &feedbackCaptureObserver{}
	c, _ := echotest.Post(t, "/v1/responses", nil)
	setResponseFeedbackObservers(c, []ext.ResponseFeedbackObserver{observer})

	notifyResponsesResponseFeedback(
		c,
		ext.EndpointResponses,
		&core.ResponsesResponse{Usage: &core.ResponsesUsage{}},
		"gpt-5.6",
		"openai",
		"primary",
	)

	require.Len(t, observer.feedback, 1)

	got := observer.feedback[0]
	require.Equal(t, 0, got.inputTokens)
	require.Equal(t, 0, got.cacheRead)
	require.Equal(t, 0, got.cacheWrite)
	require.True(t, got.usageObserved, "feedback = %+v, want confirmed zero usage", got)
}

func TestNumericIntRejectsInvalidOrOutOfRangeValues(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  int
		ok    bool
	}{
		{name: "json number", value: json.Number("2048"), want: 2048, ok: true},
		{name: "float integer", value: float64(1536), want: 1536, ok: true},
		{name: "fractional float", value: 1.5, ok: false},
		{name: "json number above int64", value: json.Number("9223372036854775808"), ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := numericInt(tt.value)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.ok, ok)
		})
	}
}

func TestResponseFeedbackStreamObserverUsesLatestUsageEvent(t *testing.T) {
	observer := &feedbackCaptureObserver{}
	ctx := core.WithRequestID(context.Background(), "req-stream")
	ctx = core.WithSessionID(ctx, "session-stream")
	streamObserver := &responseFeedbackStreamObserver{
		ctx: ctx, observers: []ext.ResponseFeedbackObserver{observer}, requestID: "req-stream", sessionID: "session-stream",
		endpoint: ext.EndpointResponses, model: "claude", providerType: "anthropic", providerName: "primary",
	}
	streamObserver.OnJSONEvent(map[string]any{
		"type": "message_start",
		"message": map[string]any{"usage": map[string]any{
			"input_tokens": float64(2000), "cache_read_input_tokens": float64(1600),
		}},
	})
	streamObserver.OnJSONEvent(map[string]any{
		"type": "response.completed",
		"response": map[string]any{"usage": map[string]any{
			"input_tokens": float64(2300), "cache_read_input_tokens": float64(1900), "cache_creation_input_tokens": float64(200),
		}},
	})
	streamObserver.OnStreamClose()

	require.Len(t, observer.feedback, 1)

	got := observer.feedback[0]
	require.Equal(t, ext.EndpointResponses, got.endpoint)
	require.Equal(t, 2300, got.inputTokens)
	require.Equal(t, 1900, got.cacheRead)
	require.Equal(t, 200, got.cacheWrite)
	require.True(t, got.usageObserved, "feedback = %+v", got)
}

func TestResponseFeedbackStreamObserverReportsUnknownUsage(t *testing.T) {
	observer := &feedbackCaptureObserver{}
	streamObserver := &responseFeedbackStreamObserver{ctx: context.Background(), observers: []ext.ResponseFeedbackObserver{observer}, endpoint: ext.EndpointChatCompletions}
	streamObserver.OnStreamClose()
	require.Len(t, observer.feedback, 1)
	require.False(t, observer.feedback[0].usageObserved)
}

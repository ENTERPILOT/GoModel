package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/core"
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
	if len(observer.feedback) != 1 {
		t.Fatalf("feedback count = %d, want 1", len(observer.feedback))
	}
	got := observer.feedback[0]
	if got.requestID != "req-1" || got.sessionID != "session-1" || got.model != "gpt-5.6" ||
		got.providerType != "anthropic" || got.providerName != "primary" || got.inputTokens != 2400 ||
		got.cacheRead != 1800 || got.cacheWrite != 300 || !got.usageObserved {
		t.Fatalf("feedback = %+v", got)
	}
}

func TestNotifyResponsesResponseFeedbackPreservesObservedZeroUsage(t *testing.T) {
	observer := &feedbackCaptureObserver{}
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c := e.NewContext(req, httptest.NewRecorder())
	setResponseFeedbackObservers(c, []ext.ResponseFeedbackObserver{observer})

	notifyResponsesResponseFeedback(
		c,
		ext.EndpointResponses,
		&core.ResponsesResponse{Usage: &core.ResponsesUsage{}},
		"gpt-5.6",
		"openai",
		"primary",
	)

	if len(observer.feedback) != 1 {
		t.Fatalf("feedback count = %d, want 1", len(observer.feedback))
	}
	got := observer.feedback[0]
	if got.inputTokens != 0 || got.cacheRead != 0 || got.cacheWrite != 0 || !got.usageObserved {
		t.Fatalf("feedback = %+v, want confirmed zero usage", got)
	}
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
			if got != tt.want || ok != tt.ok {
				t.Fatalf("numericInt(%v) = (%d, %v), want (%d, %v)", tt.value, got, ok, tt.want, tt.ok)
			}
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

	if len(observer.feedback) != 1 {
		t.Fatalf("feedback count = %d, want 1", len(observer.feedback))
	}
	got := observer.feedback[0]
	if got.endpoint != ext.EndpointResponses || got.inputTokens != 2300 || got.cacheRead != 1900 || got.cacheWrite != 200 || !got.usageObserved {
		t.Fatalf("feedback = %+v", got)
	}
}

func TestResponseFeedbackStreamObserverReportsUnknownUsage(t *testing.T) {
	observer := &feedbackCaptureObserver{}
	streamObserver := &responseFeedbackStreamObserver{ctx: context.Background(), observers: []ext.ResponseFeedbackObserver{observer}, endpoint: ext.EndpointChatCompletions}
	streamObserver.OnStreamClose()
	if len(observer.feedback) != 1 || observer.feedback[0].usageObserved {
		t.Fatalf("feedback = %+v, want one unknown-usage observation", observer.feedback)
	}
}

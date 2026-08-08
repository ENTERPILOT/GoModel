package server

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/core"
)

const responseFeedbackObserversKey = "gomodel.response-feedback-observers"

func setResponseFeedbackObservers(c *echo.Context, observers []ext.ResponseFeedbackObserver) {
	if c == nil || len(observers) == 0 {
		return
	}
	c.Set(responseFeedbackObserversKey, append([]ext.ResponseFeedbackObserver(nil), observers...))
}

func responseFeedbackObservers(c *echo.Context) []ext.ResponseFeedbackObserver {
	if c == nil {
		return nil
	}
	observers, _ := c.Get(responseFeedbackObserversKey).([]ext.ResponseFeedbackObserver)
	return observers
}

func hasResponseFeedbackObservers(c *echo.Context) bool {
	return len(responseFeedbackObservers(c)) > 0
}

type responseCacheUsage struct {
	input    int
	read     int
	write    int
	observed bool
}

func notifyChatResponseFeedback(c *echo.Context, endpoint ext.Endpoint, resp *core.ChatResponse, model, providerType, providerName string) {
	observers := responseFeedbackObservers(c)
	if len(observers) == 0 {
		return
	}
	usage := responseCacheUsage{}
	if resp != nil {
		usage = cacheUsageFromCore(resp.Usage.PromptTokens, resp.Usage.PromptTokensDetails, resp.Usage.RawUsage)
		if resp.Model != "" {
			model = resp.Model
		}
	}
	notifyResponseFeedback(c.Request().Context(), observers, core.GetRequestID(c.Request().Context()), core.SessionIDFromContext(c.Request().Context()), endpoint, model, providerType, providerName, usage)
}

func notifyResponsesResponseFeedback(c *echo.Context, endpoint ext.Endpoint, resp *core.ResponsesResponse, model, providerType, providerName string) {
	observers := responseFeedbackObservers(c)
	if len(observers) == 0 {
		return
	}
	usage := responseCacheUsage{}
	if resp != nil {
		if resp.Usage != nil {
			usage = cacheUsageFromCore(resp.Usage.InputTokens, resp.Usage.PromptTokensDetails, resp.Usage.RawUsage)
		}
		if resp.Model != "" {
			model = resp.Model
		}
	}
	notifyResponseFeedback(c.Request().Context(), observers, core.GetRequestID(c.Request().Context()), core.SessionIDFromContext(c.Request().Context()), endpoint, model, providerType, providerName, usage)
}

func notifyResponseFeedback(
	ctx context.Context,
	observers []ext.ResponseFeedbackObserver,
	requestID, sessionID string,
	endpoint ext.Endpoint,
	model, providerType, providerName string,
	usage responseCacheUsage,
) {
	for _, observer := range observers {
		if observer == nil {
			continue
		}
		func() {
			defer func() { _ = recover() }()
			observer.ObserveResponse(
				ctx, requestID, endpoint, sessionID, model, providerType, providerName,
				usage.input, usage.read, usage.write, usage.observed,
			)
		}()
	}
}

func cacheUsageFromCore(input int, details *core.PromptTokensDetails, raw map[string]any) responseCacheUsage {
	usage := responseCacheUsage{input: input, observed: input > 0 || details != nil || raw != nil}
	if details != nil {
		usage.read = details.CachedTokens
	}
	read, write := cacheTokensFromMap(raw)
	usage.read = max(usage.read, read)
	usage.write = write
	return usage
}

// responseFeedbackStreamObserver observes only usage-bearing SSE events and
// emits one summary when the stream closes. The provider bytes remain
// untouched and no response content is retained.
type responseFeedbackStreamObserver struct {
	ctx          context.Context
	observers    []ext.ResponseFeedbackObserver
	requestID    string
	sessionID    string
	endpoint     ext.Endpoint
	model        string
	providerType string
	providerName string
	usage        responseCacheUsage
}

func (o *responseFeedbackStreamObserver) WantsJSONEvent(raw []byte) bool {
	return bytes.Contains(raw, []byte(`"usage"`))
}

func (o *responseFeedbackStreamObserver) OnJSONEvent(payload map[string]any) {
	usageMap, ok := streamUsageMap(payload)
	if !ok {
		return
	}
	usage := responseCacheUsage{observed: true}
	usage.input = firstNumericInt(usageMap, "prompt_tokens", "input_tokens")
	usage.read, usage.write = cacheTokensFromMap(usageMap)
	if details, ok := nestedMap(usageMap["prompt_tokens_details"]); ok {
		usage.read = max(usage.read, firstNumericInt(details, "cached_tokens"))
	}
	if details, ok := nestedMap(usageMap["input_tokens_details"]); ok {
		usage.read = max(usage.read, firstNumericInt(details, "cached_tokens"))
	}
	o.usage = usage
}

func (o *responseFeedbackStreamObserver) OnStreamClose() {
	if o == nil || o.ctx == nil {
		return
	}
	notifyResponseFeedback(
		context.WithoutCancel(o.ctx),
		o.observers,
		o.requestID,
		o.sessionID,
		o.endpoint,
		o.model,
		o.providerType,
		o.providerName,
		o.usage,
	)
}

func streamUsageMap(payload map[string]any) (map[string]any, bool) {
	if usage, ok := nestedMap(payload["usage"]); ok {
		return usage, true
	}
	for _, key := range []string{"response", "message"} {
		container, ok := nestedMap(payload[key])
		if !ok {
			continue
		}
		if usage, ok := nestedMap(container["usage"]); ok {
			return usage, true
		}
	}
	return nil, false
}

func cacheTokensFromMap(raw map[string]any) (read, write int) {
	if len(raw) == 0 {
		return 0, 0
	}
	read = firstNumericInt(raw, "cache_read_input_tokens", "prompt_cached_tokens", "cached_tokens")
	write = firstNumericInt(raw, "cache_creation_input_tokens", "cache_write_input_tokens")
	if nested, ok := nestedMap(raw["raw_usage"]); ok {
		nestedRead, nestedWrite := cacheTokensFromMap(nested)
		read = max(read, nestedRead)
		write = max(write, nestedWrite)
	}
	return read, write
}

func firstNumericInt(values map[string]any, keys ...string) int {
	for _, key := range keys {
		if value, ok := numericInt(values[key]); ok {
			return value
		}
	}
	return 0
}

func numericInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		parsed, err := strconv.ParseInt(string(typed), 10, 64)
		return int(parsed), err == nil
	default:
		return 0, false
	}
}

func nestedMap(value any) (map[string]any, bool) {
	typed, ok := value.(map[string]any)
	return typed, ok
}

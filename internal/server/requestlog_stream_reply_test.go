package server

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/requestlog"
)

// Whichever route a streamed reply takes through the gateway, the request log entry
// must hold the text the client received. Each route hands the request log observer
// a different stream dialect, and one it cannot read leaves an empty reply
// behind while the request itself looks healthy.
func TestStreamedReplyReachesRequestLog(t *testing.T) {
	const reply = "pong-from-upstream"

	sse := func(events ...string) string {
		return strings.Join(events, "\n\n") + "\n\n"
	}
	chatSSE := sse(
		`data: {"id":"chatcmpl-1","model":"test-model","choices":[{"index":0,"delta":{"role":"assistant","content":"`+reply+`"}}]}`,
		`data: {"id":"chatcmpl-1","model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	)
	responsesSSE := sse(
		`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress","model":"test-model"}}`,
		`data: {"type":"response.output_text.delta","delta":"`+reply+`"}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"test-model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"`+reply+`"}]}]}}`,
		`data: [DONE]`,
	)
	anthropicSSE := sse(
		"event: message_start\n"+`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"test-model","content":[],"usage":{"input_tokens":7,"output_tokens":1}}}`,
		"event: content_block_start\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"event: content_block_delta\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"`+reply+`"}}`,
		"event: content_block_stop\n"+`data: {"type":"content_block_stop","index":0}`,
		"event: message_delta\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		"event: message_stop\n"+`data: {"type":"message_stop"}`,
	)
	passthrough := func(body string) *core.PassthroughResponse {
		return &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers:    map[string][]string{"Content-Type": {"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}
	}
	translated := &mockProvider{supportedModels: []string{"test-model"}}

	tests := []struct {
		name     string
		path     string
		body     string
		provider *mockProvider
		stream   string
		workflow *core.Workflow
		call     func(*Handler, *echo.Context) error
	}{
		{
			name:     "chat completions",
			path:     "/v1/chat/completions",
			body:     `{"model":"test-model","stream":true,"messages":[{"role":"user","content":"ping"}]}`,
			provider: translated,
			stream:   chatSSE,
			call:     (*Handler).ChatCompletion,
		},
		{
			name:     "responses",
			path:     "/v1/responses",
			body:     `{"model":"test-model","stream":true,"input":"ping"}`,
			provider: translated,
			stream:   responsesSSE,
			call:     (*Handler).Responses,
		},
		{
			name:     "messages translated to another provider",
			path:     "/v1/messages",
			body:     `{"model":"test-model","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"ping"}]}`,
			provider: translated,
			stream:   chatSSE,
			call:     (*Handler).Messages,
		},
		{
			name: "messages forwarded natively to Anthropic",
			path: "/v1/messages",
			body: `{"model":"test-model","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"ping"}]}`,
			provider: &mockProvider{
				supportedModels:     []string{"test-model"},
				providerTypes:       map[string]string{"test-model": "anthropic"},
				passthroughResponse: passthrough(anthropicSSE),
			},
			call: (*Handler).Messages,
		},
		{
			name:     "anthropic passthrough",
			path:     "/p/anthropic/v1/messages",
			body:     `{"model":"test-model","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"ping"}]}`,
			provider: &mockProvider{passthroughResponse: passthrough(anthropicSSE)},
			workflow: &core.Workflow{
				Mode:         core.ExecutionModePassthrough,
				ProviderType: "anthropic",
				Passthrough: &core.PassthroughRouteInfo{
					Provider:           "anthropic",
					RawEndpoint:        "messages",
					NormalizedEndpoint: "messages",
					Model:              "test-model",
					RequestLogPath:     "/v1/messages",
				},
			},
			call: (*Handler).ProviderPassthrough,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := *tt.provider
			provider.streamData = tt.stream
			logger := &capturingRequestLogger{config: requestlog.Config{Enabled: true, LogBodies: true}}
			handler := NewHandler(&provider, logger, nil, nil)

			entry := &requestlog.LogEntry{ID: "entry-1", Path: tt.path, Data: &requestlog.LogData{}}
			c, rec := echotest.Post(t, tt.path, tt.body, echotest.WithValue(string(requestlog.LogEntryKey), entry))
			if tt.workflow != nil {
				c.SetRequest(c.Request().WithContext(core.WithWorkflow(c.Request().Context(), tt.workflow)))
			}

			require.NoError(t, tt.call(handler, c))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), reply, "the client did not receive the reply")

			require.Len(t, logger.entries, 1)
			require.NotNil(t, logger.entries[0].Data)
			logged, err := json.Marshal(logger.entries[0].Data.ResponseBody)
			require.NoError(t, err)
			assert.Contains(t, string(logged), reply, "the request log entry lost the reply the client received")
		})
	}
}

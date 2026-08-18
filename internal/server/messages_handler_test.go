package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestMessages_NonStreaming(t *testing.T) {
	provider := &capturingProvider{
		mockProvider: mockProvider{
			supportedModels: []string{"claude-test"},
			response: &core.ChatResponse{
				ID:     "resp-1",
				Object: "chat.completion",
				Model:  "claude-test",
				Choices: []core.Choice{{
					Index:        0,
					Message:      core.ResponseMessage{Role: "assistant", Content: "Hello back"},
					FinishReason: "stop",
				}},
				Usage: core.Usage{PromptTokens: 9, CompletionTokens: 3, TotalTokens: 12},
			},
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)

	reqBody := `{"model":"claude-test","max_tokens":64,"system":"be brief","messages":[{"role":"user","content":"Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	if err := handler.Messages(e.NewContext(req, rec)); err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["type"] != "message" || resp["role"] != "assistant" {
		t.Errorf("envelope = %+v", resp)
	}
	content, _ := resp["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %+v", resp["content"])
	}
	block, _ := content[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "Hello back" {
		t.Errorf("content block = %+v", block)
	}
	if resp["stop_reason"] != "end_turn" {
		t.Errorf("stop_reason = %v", resp["stop_reason"])
	}

	// The Anthropic request must have been translated to the canonical chat type.
	if provider.capturedChatReq == nil {
		t.Fatal("provider did not receive a chat request")
	}
	if got := core.RequestDialectFromContext(provider.capturedChatCtx); got != core.RequestDialectAnthropicMessages {
		t.Fatalf("request dialect = %q, want %q", got, core.RequestDialectAnthropicMessages)
	}
	msgs := provider.capturedChatReq.Messages
	if len(msgs) != 2 || msgs[0].Role != "system" || msgs[1].Role != "user" {
		t.Fatalf("translated messages = %+v", msgs)
	}
}

func TestMessages_Streaming(t *testing.T) {
	chatSSE := strings.Join([]string{
		`data: {"id":"resp-2","model":"claude-test","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"content":"Hi!"},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":1}}`,
		`data: [DONE]`,
		"",
	}, "\n\n")

	provider := &capturingProvider{
		mockProvider: mockProvider{
			supportedModels: []string{"claude-test"},
			streamData:      chatSSE,
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)

	reqBody := `{"model":"claude-test","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	if err := handler.Messages(e.NewContext(req, rec)); err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	for _, want := range []string{
		"event: message_start",
		"event: content_block_start",
		`"text_delta"`,
		"event: message_delta",
		"event: message_stop",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("stream missing %q\n%s", want, body)
		}
	}
	// include_usage must be set so the converter sees the final usage chunk.
	if provider.capturedChatReq == nil || provider.capturedChatReq.StreamOptions == nil ||
		!provider.capturedChatReq.StreamOptions.IncludeUsage {
		t.Error("translated stream request did not request usage")
	}
}

func TestMessages_NativeAnthropicForwarding(t *testing.T) {
	nativeResponse := `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"native"}],"model":"claude-test","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	provider := &mockProvider{
		supportedModels: []string{"claude-test"},
		providerTypes:   map[string]string{"claude-test": "anthropic"},
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers:    map[string][]string{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(nativeResponse)),
		},
	}

	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)

	// cache_control does not survive the translated pipeline; the native path
	// must forward it verbatim.
	reqBody := `{"model":"claude-test","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"Hi","cache_control":{"type":"ephemeral"}}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-beta", "claude-code-20250219")
	rec := httptest.NewRecorder()

	if err := handler.Messages(e.NewContext(req, rec)); err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != nativeResponse {
		t.Errorf("body = %s, want provider-native response verbatim", rec.Body.String())
	}

	if provider.lastPassthroughProvider != "anthropic" {
		t.Fatalf("passthrough provider = %q, want anthropic", provider.lastPassthroughProvider)
	}
	forwarded := provider.lastPassthroughReq
	if forwarded == nil {
		t.Fatal("provider did not receive a passthrough request")
	}
	if forwarded.Endpoint != "messages" || forwarded.Method != http.MethodPost {
		t.Errorf("endpoint = %q method = %q", forwarded.Endpoint, forwarded.Method)
	}
	forwardedBody, err := io.ReadAll(forwarded.Body)
	if err != nil {
		t.Fatalf("read forwarded body: %v", err)
	}
	if string(forwardedBody) != reqBody {
		t.Errorf("forwarded body = %s, want original request verbatim", forwardedBody)
	}
	if got := forwarded.Headers.Get("anthropic-beta"); got != "claude-code-20250219" {
		t.Errorf("forwarded anthropic-beta = %q", got)
	}
}

func TestRewriteMessagesModel(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		model string
		want  string
	}{
		{
			name:  "same model leaves body untouched",
			body:  `{"model":"claude-test","max_tokens":1,"messages":[]}`,
			model: "claude-test",
			want:  `{"model":"claude-test","max_tokens":1,"messages":[]}`,
		},
		{
			name:  "empty model leaves body untouched",
			body:  `{"model":"claude-test"}`,
			model: "",
			want:  `{"model":"claude-test"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := rewriteMessagesModel([]byte(tt.body), tt.model)
			if err != nil {
				t.Fatalf("rewriteMessagesModel: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("body = %s, want %s", got, tt.want)
			}
		})
	}

	t.Run("alias rewrites model field", func(t *testing.T) {
		got, err := rewriteMessagesModel([]byte(`{"model":"my-alias","max_tokens":1}`), "claude-test")
		if err != nil {
			t.Fatalf("rewriteMessagesModel: %v", err)
		}
		var fields map[string]any
		if err := json.Unmarshal(got, &fields); err != nil {
			t.Fatalf("unmarshal rewritten body: %v", err)
		}
		if fields["model"] != "claude-test" {
			t.Errorf("model = %v, want claude-test", fields["model"])
		}
		if fields["max_tokens"] != float64(1) {
			t.Errorf("max_tokens = %v, want 1", fields["max_tokens"])
		}
	})
}

func TestMessages_InvalidRequestReturnsAnthropicError(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"claude-test"}}
	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)

	// max_tokens is required by the Anthropic dialect.
	reqBody := `{"model":"claude-test","messages":[{"role":"user","content":"Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	if err := handler.Messages(e.NewContext(req, rec)); err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["type"] != "error" {
		t.Fatalf("response is not an Anthropic error envelope: %+v", resp)
	}
	errObj, _ := resp["error"].(map[string]any)
	if errObj["type"] != "invalid_request_error" {
		t.Errorf("error type = %v, want invalid_request_error", errObj["type"])
	}
}

func TestCountMessageTokens(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"claude-test"}}
	e := echo.New()
	handler := NewHandler(provider, nil, nil, nil)

	reqBody := `{"model":"claude-test","max_tokens":64,"messages":[{"role":"user","content":"count these tokens please"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	if err := handler.CountMessageTokens(e.NewContext(req, rec)); err != nil {
		t.Fatalf("CountMessageTokens: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	tokens, ok := resp["input_tokens"].(float64)
	if !ok || tokens <= 0 {
		t.Errorf("input_tokens = %v, want > 0", resp["input_tokens"])
	}
}

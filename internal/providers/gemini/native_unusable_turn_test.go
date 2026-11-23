package gemini

import (
	"net/http"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// Gemini 3.x sometimes ends a turn with MALFORMED_RESPONSE or
// MALFORMED_FUNCTION_CALL and a candidate holding only its role, and now and
// then with STOP and no output at all. Returned as a successful empty turn,
// Claude Code stopped mid-task without retrying; a 502 makes it retry.
func TestNativeChatResponse_UnusableTurns(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantErr  string // substring of the error message; "" means no error
		wantText string
	}{
		{
			name:    "MalformedResponse",
			body:    `{"candidates":[{"content":{"role":"model"},"finishReason":"MALFORMED_RESPONSE"}],"usageMetadata":{"promptTokenCount":30,"totalTokenCount":111,"thoughtsTokenCount":81}}`,
			wantErr: "MALFORMED_RESPONSE",
		},
		{
			name:    "MalformedFunctionCallKeepsFinishMessage",
			body:    `{"candidates":[{"content":{"role":"model"},"finishReason":"MALFORMED_FUNCTION_CALL","finishMessage":"Malformed function call: get_weather("}]}`,
			wantErr: "MALFORMED_FUNCTION_CALL: Malformed function call: get_weather(",
		},
		{
			name:    "EmptyStop",
			body:    `{"candidates":[{"content":{"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":30,"totalTokenCount":30}}`,
			wantErr: "STOP with no output",
		},
		{
			name: "StopWithOutputTokensIsKept",
			body: `{"candidates":[{"content":{"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"candidatesTokenCount":3}}`,
		},
		{
			name:     "StopWithText",
			body:     `{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}]}`,
			wantText: "hi",
		},
		{
			name: "ContentlessMaxTokensIsKept",
			body: `{"candidates":[{"content":{"role":"model"},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"thoughtsTokenCount":400}}`,
		},
		{
			name:     "OneUsableCandidateIsEnough",
			body:     `{"candidates":[{"index":0,"content":{"role":"model"},"finishReason":"MALFORMED_RESPONSE"},{"index":1,"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}]}`,
			wantText: "hi",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var geminiResp geminiGenerateContentResponse
			require.NoError(t, json.Unmarshal([]byte(tt.body), &geminiResp))

			resp, err := nativeChatResponse(&core.ChatRequest{Model: "gemini-3.1-pro-preview"}, &geminiResp, "vertex")
			if tt.wantErr != "" {
				var gatewayErr *core.GatewayError
				require.ErrorAs(t, err, &gatewayErr)
				assert.Equal(t, http.StatusBadGateway, gatewayErr.StatusCode)
				assert.Equal(t, "vertex", gatewayErr.Provider)
				assert.Contains(t, gatewayErr.Message, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.NotEmpty(t, resp.Choices)
			if tt.wantText != "" {
				var texts []string
				for _, choice := range resp.Choices {
					texts = append(texts, core.ExtractTextContent(choice.Message.Content))
				}
				assert.Contains(t, texts, tt.wantText)
			}
		})
	}
}

func TestStreamChatCompletion_NativeUnusableTurns(t *testing.T) {
	tests := []struct {
		name     string
		upstream string
		wantErr  string // substring of the stream error message; "" means no error
	}{
		{
			name: "MalformedResponseFailsBeforeOutput",
			upstream: `data: {"candidates":[{"content":{"role":"model"},"finishReason":"MALFORMED_RESPONSE"}],"usageMetadata":{"thoughtsTokenCount":81}}

`,
			wantErr: "MALFORMED_RESPONSE",
		},
		{
			name: "EmptyStopFailsBeforeOutput",
			upstream: `data: {"candidates":[{"content":{"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":30}}

`,
			wantErr: "STOP with no output",
		},
		{
			name: "MalformedAfterTextClosesNormally",
			upstream: `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Checking"}]}}]}

data: {"candidates":[{"content":{"role":"model"},"finishReason":"MALFORMED_FUNCTION_CALL"}]}

`,
		},
		{
			name: "StopAfterTextClosesNormally",
			upstream: `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Done"}]}}]}

data: {"candidates":[{"content":{"role":"model"},"finishReason":"STOP"}]}

`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := streamNativeChat(t, tt.upstream)

			var errorMessages []string
			sawChoice := false
			for _, chunk := range chunks {
				if errBody, ok := chunk["error"].(map[string]any); ok {
					message, _ := errBody["message"].(string)
					errorMessages = append(errorMessages, message)
				}
				if choices, ok := chunk["choices"].([]any); ok && len(choices) > 0 {
					sawChoice = true
				}
			}

			if tt.wantErr != "" {
				require.Len(t, errorMessages, 1, "chunks %v", chunks)
				assert.Contains(t, errorMessages[0], tt.wantErr)
				assert.False(t, sawChoice, "no choice may precede the error")
				return
			}
			assert.Empty(t, errorMessages)
			assert.True(t, sawChoice)
		})
	}
}

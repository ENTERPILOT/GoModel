package gemini

import (
	"io"
	"net/http"
	"strings"
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

// A streamed turn is judged when the stream ends, once every choice's finish
// reason is known, so an unusable turn fails even after text or a role-only
// chunk has been written.
func TestStreamChatCompletion_NativeUnusableTurns(t *testing.T) {
	tests := []struct {
		name       string
		upstream   string
		wantErr    string // substring of the stream error message; "" means no error
		wantText   string // text streamed before the end, if any
		wantFinish string // the finish_reason when there is no error
	}{
		{
			name: "MalformedResponseWithNoOutput",
			upstream: `data: {"candidates":[{"content":{"role":"model"},"finishReason":"MALFORMED_RESPONSE"}],"usageMetadata":{"thoughtsTokenCount":81}}

`,
			wantErr: "MALFORMED_RESPONSE",
		},
		{
			name: "EmptyStopWithNoOutput",
			upstream: `data: {"candidates":[{"content":{"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":30}}

`,
			wantErr: "STOP with no output",
		},
		{
			name: "MalformedCallAfterTextFails",
			upstream: `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Let me read the file."}]}}]}

data: {"candidates":[{"content":{"role":"model"},"finishReason":"MALFORMED_FUNCTION_CALL","finishMessage":"Malformed function call: read_file("}]}

`,
			wantErr:  "MALFORMED_FUNCTION_CALL: Malformed function call: read_file(",
			wantText: "Let me read the file.",
		},
		{
			name: "EmptyFirstEventThenEmptyStopFails",
			upstream: `data: {"candidates":[{"content":{"role":"model"}}]}

data: {"candidates":[{"content":{"role":"model"},"finishReason":"STOP"}]}

`,
			wantErr: "STOP with no output",
		},
		{
			name: "ThoughtOnlyFirstEventThenMalformedFails",
			upstream: `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Planning","thought":true}]}}]}

data: {"candidates":[{"content":{"role":"model"},"finishReason":"MALFORMED_FUNCTION_CALL"}]}

`,
			wantErr: "MALFORMED_FUNCTION_CALL",
		},
		{
			name: "StopAfterTextClosesNormally",
			upstream: `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Done"}]}}]}

data: {"candidates":[{"content":{"role":"model"},"finishReason":"STOP"}]}

`,
			wantText:   "Done",
			wantFinish: "stop",
		},
		{
			name: "EmptyFirstEventThenTextClosesNormally",
			upstream: `data: {"candidates":[{"content":{"role":"model"}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Done"}]},"finishReason":"STOP"}]}

`,
			wantText:   "Done",
			wantFinish: "stop",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := streamNativeChat(t, tt.upstream)

			var errorMessages, finishes []string
			var text strings.Builder
			errorIndex, lastChoiceIndex := -1, -1
			for i, chunk := range chunks {
				if errBody, ok := chunk["error"].(map[string]any); ok {
					message, _ := errBody["message"].(string)
					errorMessages = append(errorMessages, message)
					errorIndex = i
				}
				choices, _ := chunk["choices"].([]any)
				for _, entry := range choices {
					lastChoiceIndex = i
					choice, ok := entry.(map[string]any)
					require.True(t, ok, "choice = %#v", entry)
					if finish, ok := choice["finish_reason"].(string); ok {
						finishes = append(finishes, finish)
					}
					delta, _ := choice["delta"].(map[string]any)
					content, _ := delta["content"].(string)
					text.WriteString(content)
				}
			}

			assert.Equal(t, tt.wantText, text.String())
			if tt.wantErr != "" {
				require.Len(t, errorMessages, 1, "chunks %v", chunks)
				assert.Contains(t, errorMessages[0], tt.wantErr)
				assert.Empty(t, finishes, "an unusable turn must not finish")
				assert.Greater(t, errorIndex, lastChoiceIndex, "the error ends the stream")
				return
			}
			assert.Empty(t, errorMessages)
			assert.Equal(t, []string{tt.wantFinish}, finishes)
		})
	}
}

// Gemini bills the prompt and thinking tokens of an unusable turn, so the
// stream still reports its usage, before the error, for the usage observer.
func TestGeminiNativeStream_UnusableTurnReportsUsage(t *testing.T) {
	upstream := `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Let me read the file."}]}}],"usageMetadata":{"promptTokenCount":30}}

data: {"candidates":[{"content":{"role":"model"},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":30,"thoughtsTokenCount":81,"totalTokenCount":111}}

`
	stream := newGeminiNativeStream(io.NopCloser(strings.NewReader(upstream)), "gemini-2.5-pro", true, "vertex")
	defer func() { _ = stream.Close() }()
	raw, err := io.ReadAll(stream)
	require.NoError(t, err)
	chunks := parseOpenAIStreamChunks(t, string(raw))

	usageIndex, errorIndex := -1, -1
	var usage map[string]any
	for i, chunk := range chunks {
		if u, ok := chunk["usage"].(map[string]any); ok {
			usageIndex, usage = i, u
		}
		if _, ok := chunk["error"].(map[string]any); ok {
			errorIndex = i
		}
	}
	require.NotEqual(t, -1, usageIndex, "chunks %v", chunks)
	require.NotEqual(t, -1, errorIndex, "chunks %v", chunks)
	assert.Less(t, usageIndex, errorIndex, "usage comes before the error")
	assert.InDelta(t, 30, usage["prompt_tokens"], 0)
	assert.InDelta(t, 111, usage["total_tokens"], 0)
}

func TestUnusableTurnError(t *testing.T) {
	longCall := "Malformed function call: " + strings.Repeat("x", 5000)
	tests := []struct {
		name         string
		endings      []turnEnding
		outputTokens int
		wantErr      string // the error message after its fixed prefix; "" means no error
	}{
		{
			name: "NoEndings",
		},
		{
			name:    "MalformedFunctionCall",
			endings: []turnEnding{{finishReason: "MALFORMED_FUNCTION_CALL"}},
			wantErr: "MALFORMED_FUNCTION_CALL",
		},
		{
			name:    "MalformedAfterOutputIsStillUnusable",
			endings: []turnEnding{{finishReason: "MALFORMED_FUNCTION_CALL", hasOutput: true}},
			wantErr: "MALFORMED_FUNCTION_CALL",
		},
		{
			name:    "ReasonIsCaseInsensitive",
			endings: []turnEnding{{finishReason: " malformed_response "}},
			wantErr: "MALFORMED_RESPONSE",
		},
		{
			name:    "EmptyStop",
			endings: []turnEnding{{finishReason: "STOP"}},
			wantErr: "STOP with no output",
		},
		{
			name:    "StopWithOutput",
			endings: []turnEnding{{finishReason: "STOP", hasOutput: true}},
		},
		{
			name:         "StopWithOutputTokens",
			endings:      []turnEnding{{finishReason: "STOP"}},
			outputTokens: 3,
		},
		{
			name:    "NoFinishReason",
			endings: []turnEnding{{}},
		},
		{
			name:    "OneUsableEndingIsEnough",
			endings: []turnEnding{{finishReason: "MALFORMED_RESPONSE"}, {finishReason: "STOP", hasOutput: true}},
		},
		{
			name:    "FinishMessageIsKept",
			endings: []turnEnding{{finishReason: "MALFORMED_FUNCTION_CALL", finishMessage: " Malformed function call: get_weather( "}},
			wantErr: "MALFORMED_FUNCTION_CALL: Malformed function call: get_weather(",
		},
		{
			name:    "LongFinishMessageIsTruncated",
			endings: []turnEnding{{finishReason: "MALFORMED_FUNCTION_CALL", finishMessage: longCall}},
			wantErr: "MALFORMED_FUNCTION_CALL: " + longCall[:maxFailureTextLen] + "...",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := unusableTurnError(tt.endings, tt.outputTokens, "vertex")
			if tt.wantErr == "" {
				assert.Nil(t, err)
				return
			}
			require.NotNil(t, err)
			assert.Equal(t, http.StatusBadGateway, err.StatusCode)
			assert.Equal(t, "vertex", err.Provider)
			assert.Equal(t, "Gemini returned no usable output: "+tt.wantErr, err.Message)
		})
	}
}

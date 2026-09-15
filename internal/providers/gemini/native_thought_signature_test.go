package gemini

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

const testExtraContent = `{"google":{"thought_signature":"sig-1"}}`

func toolCallWithSignature(id, name, signature string) core.ToolCall {
	return core.ToolCall{
		ID:          id,
		Type:        "function",
		Function:    core.FunctionCall{Name: name, Arguments: `{"city":"Warsaw"}`},
		ExtraFields: thoughtSignatureExtraFields(signature),
	}
}

func rawFields(pairs map[string]string) core.UnknownJSONFields {
	fields := make(map[string]json.RawMessage, len(pairs))
	for key, value := range pairs {
		fields[key] = json.RawMessage(value)
	}
	return core.UnknownJSONFieldsFromMap(fields)
}

func TestConvertChatRequestToGemini_ThoughtSignatures(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		message  core.Message
		wantSigs []string // per part, in order
	}{
		{
			name:  "signed tool call is sent back verbatim",
			model: "gemini-3.5-flash",
			message: core.Message{Role: "assistant", ToolCalls: []core.ToolCall{
				toolCallWithSignature("call_1", "lookup_weather", "sig-1"),
			}},
			wantSigs: []string{"sig-1"},
		},
		{
			name:  "unsigned tool call on Gemini 3 gets the validator placeholder",
			model: "gemini-3.5-flash",
			message: core.Message{Role: "assistant", ToolCalls: []core.ToolCall{
				toolCallWithSignature("call_1", "lookup_weather", ""),
			}},
			wantSigs: []string{skipThoughtSignatureValidator},
		},
		{
			name:  "unsigned tool call on Gemini 2.5 stays unsigned",
			model: "gemini-2.5-flash",
			message: core.Message{Role: "assistant", ToolCalls: []core.ToolCall{
				toolCallWithSignature("call_1", "lookup_weather", ""),
			}},
			wantSigs: []string{""},
		},
		{
			name:  "parallel batch keeps only its first call signed",
			model: "gemini-3.5-flash",
			message: core.Message{Role: "assistant", Content: "Checking both.", ToolCalls: []core.ToolCall{
				toolCallWithSignature("call_1", "lookup_weather", "sig-1"),
				toolCallWithSignature("call_2", "lookup_weather", ""),
			}},
			wantSigs: []string{"", "sig-1", ""},
		},
		{
			name:  "flat snake_case spelling on the tool call",
			model: "gemini-3.5-flash",
			message: core.Message{Role: "assistant", ToolCalls: []core.ToolCall{{
				ID: "call_1", Type: "function",
				Function:    core.FunctionCall{Name: "lookup_weather", Arguments: "{}"},
				ExtraFields: rawFields(map[string]string{"thought_signature": `"sig-flat"`}),
			}}},
			wantSigs: []string{"sig-flat"},
		},
		{
			name:  "flat camelCase spelling nested on the function object",
			model: "gemini-3.5-flash",
			message: core.Message{Role: "assistant", ToolCalls: []core.ToolCall{{
				ID: "call_1", Type: "function",
				Function: core.FunctionCall{
					Name: "lookup_weather", Arguments: "{}",
					ExtraFields: rawFields(map[string]string{"thoughtSignature": `"sig-func"`}),
				},
			}}},
			wantSigs: []string{"sig-func"},
		},
		{
			name:  "malformed extra_content falls back to the placeholder",
			model: "gemini-3.5-flash",
			message: core.Message{Role: "assistant", ToolCalls: []core.ToolCall{{
				ID: "call_1", Type: "function",
				Function:    core.FunctionCall{Name: "lookup_weather", Arguments: "{}"},
				ExtraFields: rawFields(map[string]string{"extra_content": `{"google":"not-an-object"}`}),
			}}},
			wantSigs: []string{skipThoughtSignatureValidator},
		},
		{
			name:  "mixed turn keeps the text signature and the call signature",
			model: "gemini-3.5-flash",
			message: core.Message{
				Role:        "assistant",
				Content:     "Checking.",
				ExtraFields: thoughtSignatureExtraFields("sig-text"),
				ToolCalls:   []core.ToolCall{toolCallWithSignature("call_1", "lookup_weather", "sig-1")},
			},
			wantSigs: []string{"sig-text", "sig-1"},
		},
		{
			name:  "text turn signature lands on the last part",
			model: "gemini-3.5-flash",
			message: core.Message{
				Role:        "assistant",
				Content:     "Hello",
				ExtraFields: thoughtSignatureExtraFields("sig-text"),
			},
			wantSigs: []string{"sig-text"},
		},
		{
			name:  "user extra_content is ignored",
			model: "gemini-3.5-flash",
			message: core.Message{
				Role:        "user",
				Content:     "Hello",
				ExtraFields: thoughtSignatureExtraFields("sig-user"),
			},
			wantSigs: []string{""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := convertChatRequestToGemini(&core.ChatRequest{
				Model:    tt.model,
				Messages: []core.Message{{Role: "user", Content: "Weather?"}, tt.message},
			})
			require.NoError(t, err)

			parts := req.Contents[len(req.Contents)-1].Parts
			require.Len(t, parts, len(tt.wantSigs), "parts = %+v", parts)
			for i, want := range tt.wantSigs {
				assert.Equal(t, want, parts[i].ThoughtSignature, "part %d", i)
			}
		})
	}
}

func TestNativeChatResponse_ExposesThoughtSignatures(t *testing.T) {
	var geminiResp geminiGenerateContentResponse
	err := json.Unmarshal([]byte(`{"candidates":[{"content":{"role":"model","parts":[
		{"text":"Let me check.","thoughtSignature":"sig-text"},
		{"functionCall":{"id":"call_1","name":"lookup_weather","args":{"city":"Warsaw"}},"thoughtSignature":"sig-1"},
		{"functionCall":{"id":"call_2","name":"lookup_weather","args":{"city":"Krakow"}}}
	]},"finishReason":"STOP"}]}`), &geminiResp)
	require.NoError(t, err)

	resp, err := nativeChatResponse(&core.ChatRequest{Model: "gemini-3.5-flash"}, &geminiResp, "gemini")
	require.NoError(t, err)

	encoded, err := json.Marshal(resp.Choices[0].Message)
	require.NoError(t, err)

	var wire struct {
		ExtraContent json.RawMessage `json:"extra_content"`
		ToolCalls    []struct {
			ExtraContent json.RawMessage `json:"extra_content"`
		} `json:"tool_calls"`
	}
	err = json.Unmarshal(encoded, &wire)
	require.NoError(t, err)
	assert.Equal(t, `{"google":{"thought_signature":"sig-text"}}`, string(wire.ExtraContent))
	require.Len(t, wire.ToolCalls, 2)
	assert.Equal(t, testExtraContent, string(wire.ToolCalls[0].ExtraContent))
	assert.Empty(t, string(wire.ToolCalls[1].ExtraContent))
}

// TestChatCompletion_NativeThoughtSignatureRoundTrip replays a Gemini tool
// call exactly as an OpenAI-compatible client would: the tool_calls from the
// first response, re-decoded from JSON, are sent back in the next request and
// must reach Gemini with their original thoughtSignature.
func TestChatCompletion_NativeThoughtSignatureRoundTrip(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	// Declared up front so the handler can branch on the request count.
	var capture *providertest.Capture
	server, capture := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if capture.Count() == 1 {
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[
				{"functionCall":{"id":"call_1","name":"lookup_weather","args":{"city":"Warsaw"}},"thoughtSignature":"sig-1"}
			]},"finishReason":"STOP"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"Sunny."}]},"finishReason":"STOP"}]}`))
	})

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	first, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "gemini-3.5-flash",
		Messages: []core.Message{{Role: "user", Content: "Weather?"}},
	})
	require.NoError(t, err)

	// Serialize the assistant message the way it leaves the gateway and decode
	// it the way the next request arrives.
	encoded, err := json.Marshal(first.Choices[0].Message)
	require.NoError(t, err)

	var assistant core.Message
	err = json.Unmarshal(encoded, &assistant)
	require.NoError(t, err)
	_, err = provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gemini-3.5-flash",
		Messages: []core.Message{
			{Role: "user", Content: "Weather?"},
			assistant,
			{Role: "tool", ToolCallID: "call_1", Content: `{"result":"sunny"}`},
		},
	})
	require.NoError(t, err)

	requests := capture.All()
	require.Len(t, requests, 2)
	var payload geminiGenerateContentRequest
	require.NoError(t, json.Unmarshal(requests[1].Body, &payload))
	require.Len(t, payload.Contents, 3)

	call := payload.Contents[1].Parts[0]
	require.NotNil(t, call.FunctionCall, "model part = %+v, want functionCall", call)
	assert.Equal(t, "lookup_weather", call.FunctionCall.Name)
	assert.Equal(t, "sig-1", call.ThoughtSignature)
}

func TestStreamChatCompletion_NativeThoughtSignatures(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server, _ := providertest.SSEServer(t, `data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"lookup_weather","args":{"city":"Warsaw"}},"thoughtSignature":"sig-1"}]},"finishReason":"STOP"}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"","thoughtSignature":"sig-text"}]},"finishReason":"STOP"}]}

`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	body, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "gemini-3.5-flash",
		Messages: []core.Message{{Role: "user", Content: "Weather?"}},
	})
	require.NoError(t, err)

	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	require.NoError(t, err)

	stream := string(raw)
	assert.Contains(t, stream, `"tool_calls":[{"extra_content":{"google":{"thought_signature":"sig-1"}}`)
	assert.Contains(t, stream, `"delta":{"extra_content":{"google":{"thought_signature":"sig-text"}}}`)
}

func TestNativeChatResponse_ToolCallOnlyTurnHasNullContent(t *testing.T) {
	var geminiResp geminiGenerateContentResponse
	err := json.Unmarshal([]byte(`{"candidates":[{"content":{"role":"model","parts":[
		{"functionCall":{"id":"call_1","name":"lookup_weather","args":{}}}
	]},"finishReason":"STOP"}]}`), &geminiResp)
	require.NoError(t, err)

	resp, err := nativeChatResponse(&core.ChatRequest{Model: "gemini-3.5-flash"}, &geminiResp, "gemini")
	require.NoError(t, err)
	require.Len(t, resp.Choices, 1)
	assert.Nil(t, resp.Choices[0].Message.Content)
}

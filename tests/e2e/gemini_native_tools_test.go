//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/gemini"
)

const geminiToolModel = "gemini-3-pro-preview"

// mockGeminiNativeServer stands in for Gemini's native generateContent API and
// enforces the rule Gemini 3 enforces: a functionCall part replayed in the
// conversation history must carry back the thoughtSignature it was returned
// with, or the whole request fails with HTTP 400.
type mockGeminiNativeServer struct {
	server   *httptest.Server
	requests [][]byte
}

func newMockGeminiNativeServer(t *testing.T) *mockGeminiNativeServer {
	t.Helper()

	mock := &mockGeminiNativeServer{}
	mock.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/models" {
			_, _ = fmt.Fprintf(w, `{"models":[{"name":"models/%s","supportedGenerationMethods":["generateContent","streamGenerateContent"]}]}`, geminiToolModel)
			return
		}
		if !strings.HasSuffix(r.URL.Path, ":generateContent") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
			return
		}

		body, _ := io.ReadAll(r.Body)
		mock.requests = append(mock.requests, body)

		position, ok := unsignedFunctionCallPosition(t, body)
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":`+
				`"Function call is missing a thought_signature in functionCall parts. This is required for tools to work correctly, `+
				`and missing thought_signature may lead to degraded model performance. Additional data, function call `+
				`default_api:ExecCommand, position %d."}}`, position)
			return
		}

		if requestHasToolResult(t, body) {
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"go.mod is there."}]},` +
				`"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":5,"totalTokenCount":14},` +
				`"responseId":"resp-2"}`))
			return
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[` +
			`{"functionCall":{"name":"ExecCommand","args":{"cmd":"ls"}},"thoughtSignature":"sig-e2e-1"}` +
			`]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":11,"totalTokenCount":18},` +
			`"responseId":"resp-1"}`))
	}))
	t.Cleanup(mock.server.Close)
	return mock
}

// geminiRequestBody is the slice of the native request the signature rule
// applies to.
type geminiRequestBody struct {
	Contents []struct {
		Role  string `json:"role"`
		Parts []struct {
			FunctionCall     json.RawMessage `json:"functionCall"`
			FunctionResponse json.RawMessage `json:"functionResponse"`
			ThoughtSignature string          `json:"thoughtSignature"`
		} `json:"parts"`
	} `json:"contents"`
}

func decodeGeminiRequest(t *testing.T, body []byte) geminiRequestBody {
	t.Helper()
	var decoded geminiRequestBody
	require.NoError(t, json.Unmarshal(body, &decoded), "upstream request body: %s", body)
	return decoded
}

// unsignedFunctionCallPosition reports the part position of the first replayed
// functionCall without a thoughtSignature, mirroring Gemini 3's validation.
func unsignedFunctionCallPosition(t *testing.T, body []byte) (int, bool) {
	t.Helper()
	position := 0
	for _, content := range decodeGeminiRequest(t, body).Contents {
		for _, part := range content.Parts {
			if len(part.FunctionCall) > 0 && part.ThoughtSignature == "" {
				return position, false
			}
			position++
		}
	}
	return 0, true
}

func requestHasToolResult(t *testing.T, body []byte) bool {
	t.Helper()
	for _, content := range decodeGeminiRequest(t, body).Contents {
		for _, part := range content.Parts {
			if len(part.FunctionResponse) > 0 {
				return true
			}
		}
	}
	return false
}

func setupGeminiNativeGateway(t *testing.T) (*httptest.Server, *mockGeminiNativeServer) {
	t.Helper()

	t.Setenv("USE_GOOGLE_GEMINI_NATIVE_API", "true")
	upstream := newMockGeminiNativeServer(t)

	provider := gemini.NewWithHTTPClient("sk-test-gemini-key", upstream.server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(upstream.server.URL)
	provider.SetModelsURL(upstream.server.URL)

	registry := providers.NewModelRegistry()
	registry.RegisterProviderWithType(provider, "gemini")
	require.NoError(t, registry.Initialize(context.Background()))

	gateway := httptest.NewServer(setupE2EServer(t, e2eServerOptions{registry: registry}))
	t.Cleanup(gateway.Close)
	return gateway, upstream
}

func postGeminiChat(t *testing.T, gateway *httptest.Server, payload any) (*http.Response, []byte) {
	t.Helper()

	body, err := json.Marshal(payload)
	require.NoError(t, err)
	resp, err := gateway.Client().Post(gateway.URL+chatCompletionsPath, "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer closeBody(resp)
	responseBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, responseBody
}

// A multi-step agent turn: the client replays the assistant tool call the
// gateway handed it, and the second request must reach Gemini with the
// function call's thought signature intact.
func TestGeminiNativeToolCallsPreserveThoughtSignature(t *testing.T) {
	gateway, upstream := setupGeminiNativeGateway(t)

	tools := []map[string]any{{
		"type": "function",
		"function": map[string]any{
			"name":        "ExecCommand",
			"description": "Run a shell command.",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"cmd": map[string]any{"type": "string"}},
				"required":   []string{"cmd"},
			},
		},
	}}

	resp, body := postGeminiChat(t, gateway, map[string]any{
		"model":    geminiToolModel,
		"messages": []map[string]any{{"role": "user", "content": "is go.mod there?"}},
		"tools":    tools,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode, "first turn: %s", body)

	var first core.ChatResponse
	require.NoError(t, json.Unmarshal(body, &first))
	require.Len(t, first.Choices, 1)
	require.Len(t, first.Choices[0].Message.ToolCalls, 1)

	// The client replays the assistant message exactly as the gateway
	// returned it — the only history an OpenAI-compatible agent has.
	var assistant map[string]any
	require.NoError(t, json.Unmarshal(mustMarshal(t, first.Choices[0].Message), &assistant))

	resp, body = postGeminiChat(t, gateway, map[string]any{
		"model": geminiToolModel,
		"messages": []any{
			map[string]any{"role": "user", "content": "is go.mod there?"},
			assistant,
			map[string]any{
				"role":         "tool",
				"tool_call_id": first.Choices[0].Message.ToolCalls[0].ID,
				"content":      `{"stdout":"go.mod"}`,
			},
		},
		"tools": tools,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode, "second turn rejected upstream: %s", body)

	require.Len(t, upstream.requests, 2)
	replay := decodeGeminiRequest(t, upstream.requests[1])
	require.Len(t, replay.Contents, 3)
	modelParts := replay.Contents[1].Parts
	require.Len(t, modelParts, 1)
	require.NotEmpty(t, modelParts[0].FunctionCall)
	assert.Equal(t, "sig-e2e-1", modelParts[0].ThoughtSignature,
		"the replayed functionCall part lost its thought signature")
}

// The same round trip over the Responses API, where the client replays
// function_call items instead of chat messages.
func TestGeminiNativeResponsesToolCallsPreserveThoughtSignature(t *testing.T) {
	gateway, upstream := setupGeminiNativeGateway(t)

	tools := []map[string]any{{
		"type":        "function",
		"name":        "ExecCommand",
		"description": "Run a shell command.",
		"parameters": map[string]any{
			"type":       "object",
			"properties": map[string]any{"cmd": map[string]any{"type": "string"}},
			"required":   []string{"cmd"},
		},
	}}

	body, err := json.Marshal(map[string]any{
		"model": geminiToolModel,
		"input": "is go.mod there?",
		"tools": tools,
	})
	require.NoError(t, err)
	resp, err := gateway.Client().Post(gateway.URL+responsesPath, "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer closeBody(resp)
	first, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "first turn: %s", first)

	var firstResp struct {
		Output []map[string]any `json:"output"`
	}
	require.NoError(t, json.Unmarshal(first, &firstResp))
	var functionCall map[string]any
	for _, item := range firstResp.Output {
		if item["type"] == "function_call" {
			functionCall = item
		}
	}
	require.NotNil(t, functionCall, "output = %s, want a function_call item", first)

	input := []any{
		map[string]any{"type": "message", "role": "user", "content": "is go.mod there?"},
		functionCall,
		map[string]any{"type": "function_call_output", "call_id": functionCall["call_id"], "output": `{"stdout":"go.mod"}`},
	}
	body, err = json.Marshal(map[string]any{"model": geminiToolModel, "input": input, "tools": tools})
	require.NoError(t, err)
	resp, err = gateway.Client().Post(gateway.URL+responsesPath, "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer closeBody(resp)
	second, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "second turn rejected upstream: %s", second)

	require.Len(t, upstream.requests, 2)
	replay := decodeGeminiRequest(t, upstream.requests[1])
	require.Len(t, replay.Contents, 3)
	require.Len(t, replay.Contents[1].Parts, 1)
	assert.Equal(t, "sig-e2e-1", replay.Contents[1].Parts[0].ThoughtSignature,
		"the replayed function_call item lost its thought signature")
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	require.NoError(t, err)
	return body
}

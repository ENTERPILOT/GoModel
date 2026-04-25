//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"gomodel/internal/core"
)

// API endpoints
const (
	chatCompletionsPath = "/v1/chat/completions"
	responsesPath       = "/v1/responses"
	modelsPath          = "/v1/models"
	healthPath          = "/health"
)

func defaultChatReq(msg string) core.ChatRequest {
	return core.ChatRequest{
		Model:    "gpt-4",
		Messages: []core.Message{{Role: "user", Content: msg}},
	}
}

// sendChatRequest sends a chat completion request and returns the response.
func sendChatRequest(t *testing.T, payload core.ChatRequest) *http.Response {
	t.Helper()
	return sendJSONRequest(t, gatewayURL+chatCompletionsPath, payload)
}

// sendRawChatRequest sends a raw chat request (for testing invalid payloads).
func sendRawChatRequest(t *testing.T, payload interface{}) *http.Response {
	t.Helper()
	return sendJSONRequest(t, gatewayURL+chatCompletionsPath, payload)
}

// sendResponsesRequest sends a responses API request and returns the response.
func sendResponsesRequest(t *testing.T, payload core.ResponsesRequest) *http.Response {
	t.Helper()
	return sendJSONRequest(t, gatewayURL+responsesPath, payload)
}

// sendRawResponsesRequest sends a raw responses request (for testing invalid payloads).
func sendRawResponsesRequest(t *testing.T, payload interface{}) *http.Response {
	t.Helper()
	return sendJSONRequest(t, gatewayURL+responsesPath, payload)
}

// sendJSONRequest sends a JSON POST request and returns the response.
func sendJSONRequest(t *testing.T, url string, payload interface{}) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	require.NoError(t, err)

	return resp
}

func requireErrorResponse(t *testing.T, resp *http.Response, status int, errorType core.ErrorType, messageContains string) {
	t.Helper()

	require.Equal(t, status, resp.StatusCode)

	var envelope core.OpenAIErrorEnvelope
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&envelope))
	require.Equal(t, errorType, envelope.Error.Type)
	if messageContains != "" {
		require.Contains(t, envelope.Error.Message, messageContains)
	}
}

// sendJSONRequestNoT sends a JSON POST request without using testing.T.
//
// This is specifically for concurrency tests, where calling t.FailNow / require from
// goroutines is unsafe.
func sendJSONRequestNoT(url string, payload interface{}) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	return client.Do(req)
}

// closeBody is a helper to close response body in defer statements.
func closeBody(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}

func requireRecordedRequest(t *testing.T, path string) RecordedRequest {
	t.Helper()

	requests := mockServer.Requests()
	require.Len(t, requests, 1)
	require.Equal(t, path, requests[0].Path)
	return requests[0]
}

func requireRecordedChatRequest(t *testing.T) core.ChatRequest {
	t.Helper()

	recorded := requireRecordedRequest(t, "/chat/completions")
	var upstream core.ChatRequest
	require.NoError(t, json.Unmarshal(recorded.Body, &upstream))
	return upstream
}

func requireRecordedResponsesRequest(t *testing.T) core.ResponsesRequest {
	t.Helper()

	recorded := requireRecordedRequest(t, "/responses")
	var upstream core.ResponsesRequest
	require.NoError(t, json.Unmarshal(recorded.Body, &upstream))
	return upstream
}

// StreamChunk represents a parsed streaming chunk for chat completions.
type StreamChunk struct {
	ID      string                   `json:"id"`
	Object  string                   `json:"object"`
	Model   string                   `json:"model"`
	Created int64                    `json:"created"`
	Choices []map[string]interface{} `json:"choices"`
	Done    bool
}

const sseScannerMaxTokenSize = 4 * 1024 * 1024

// readStreamingResponse reads and parses SSE streaming response for chat completions.
func readStreamingResponse(t *testing.T, body io.Reader) []StreamChunk {
	t.Helper()
	chunks := make([]StreamChunk, 0)
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), sseScannerMaxTokenSize)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			chunks = append(chunks, StreamChunk{Done: true})
			break
		}

		var chunk StreamChunk
		require.NoError(t, json.Unmarshal([]byte(data), &chunk))
		chunks = append(chunks, chunk)
	}
	require.NoError(t, scanner.Err())

	return chunks
}

// ResponsesStreamEvent represents a streaming event from the Responses API.
type ResponsesStreamEvent struct {
	Type string                 `json:"type"`
	Data map[string]interface{} `json:"data,omitempty"`
	Done bool
}

// readResponsesStream reads and parses SSE streaming response for Responses API.
func readResponsesStream(t *testing.T, body io.Reader) []ResponsesStreamEvent {
	t.Helper()
	events := make([]ResponsesStreamEvent, 0)
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), sseScannerMaxTokenSize)

	var currentEvent ResponsesStreamEvent
	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "event: ") {
			currentEvent.Type = strings.TrimPrefix(line, "event: ")
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				events = append(events, ResponsesStreamEvent{Done: true})
				break
			}

			var eventData map[string]interface{}
			require.NoError(t, json.Unmarshal([]byte(data), &eventData))
			currentEvent.Data = eventData
			if currentEvent.Type == "" {
				if typ, ok := eventData["type"].(string); ok {
					currentEvent.Type = typ
				}
			}
			events = append(events, currentEvent)
			currentEvent = ResponsesStreamEvent{}
		}
	}
	require.NoError(t, scanner.Err())

	return events
}

// extractStreamContent extracts text content from chat streaming chunks.
func extractStreamContent(chunks []StreamChunk) string {
	var content strings.Builder
	for _, chunk := range chunks {
		if !chunk.Done && len(chunk.Choices) > 0 {
			if delta, ok := chunk.Choices[0]["delta"].(map[string]interface{}); ok {
				if text, ok := delta["content"].(string); ok {
					content.WriteString(text)
				}
			}
		}
	}
	return content.String()
}

// extractResponsesStreamContent extracts text content from responses streaming events.
func extractResponsesStreamContent(events []ResponsesStreamEvent) string {
	var content strings.Builder
	for _, event := range events {
		if event.Type == "response.output_text.delta" {
			if delta, ok := event.Data["delta"].(string); ok {
				content.WriteString(delta)
			}
		}
	}
	return content.String()
}

// hasDoneEvent checks if the stream contains a done event.
func hasDoneEvent(events []ResponsesStreamEvent) bool {
	for _, event := range events {
		if event.Type == "response.completed" || event.Type == "response.done" || event.Done {
			return true
		}
	}
	return false
}

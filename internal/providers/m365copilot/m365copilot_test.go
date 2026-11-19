package m365copilot

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

const (
	conversationID = "0d110e7e-2b7e-4270-a899-fd2af6fde333"
	chatPath       = "/conversations/" + conversationID + "/chat"
	streamPath     = "/conversations/" + conversationID + "/chatOverStream"
	prompt         = "What meeting do I have at 9 AM tomorrow morning?"
	reply          = "You have **1 meeting**: <Event>Contoso Engineering Standup</Event>[^1^]"
)

// createdConversation is the reference's reply to POST /conversations.
const createdConversation = `{"id":"` + conversationID + `","createdDateTime":"2025-09-30T15:28:46.1560062Z","displayName":"","status":"active","turnCount":0}`

// chatConversation is the reference's reply to the chat action, cut to the
// members the provider reads plus one of each it ignores: the service echoes
// the prompt as the first message and appends Copilot's reply.
const chatConversation = `{
  "id": "` + conversationID + `",
  "createdDateTime": "2025-09-30T15:55:53.4711746Z",
  "displayName": "` + prompt + `",
  "state": "active",
  "turnCount": 1,
  "messages": [
    {
      "@odata.type": "#microsoft.graph.copilotConversationResponseMessage",
      "id": "cc211f56-1a5e-0af0-fec2-c354ce468b95",
      "text": "` + prompt + `",
      "adaptiveCards": [],
      "attributions": []
    },
    {
      "@odata.type": "#microsoft.graph.copilotConversationResponseMessage",
      "id": "3fe6b260-c682-4f8e-a201-022ccb300742",
      "text": "` + reply + `",
      "adaptiveCards": [{}],
      "attributions": [{"attributionType": "citation", "providerDisplayName": "Contoso Engineering Standup", "seeMoreWebUrl": "https://teams.microsoft.com/l/meeting/details"}]
    }
  ]
}`

func jsonHandler(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func sseHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}
}

// copilotServer fakes the Chat API: conversation creation plus the two
// actions that continue it.
func copilotServer(t *testing.T, chat, stream http.HandlerFunc) (*Provider, *providertest.Capture) {
	t.Helper()
	srv, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/conversations": jsonHandler(http.StatusCreated, createdConversation),
		chatPath:         chat,
		streamPath:       stream,
	})
	return newTestProvider("entra-token", srv.URL, srv.Client(), llmclient.Hooks{}), capture
}

func userRequest(text string) *core.ChatRequest {
	return &core.ChatRequest{Model: defaultModel, Messages: []core.Message{{Role: "user", Content: text}}}
}

func extraFields(t *testing.T, fields map[string]string) core.UnknownJSONFields {
	t.Helper()
	raw := make(map[string]json.RawMessage, len(fields))
	for name, value := range fields {
		raw[name] = json.RawMessage(value)
	}
	return core.UnknownJSONFieldsFromMap(raw)
}

func TestRegistration(t *testing.T) {
	assert.Equal(t, "m365-copilot", Registration.Type)
	assert.NotNil(t, Registration.New)
	assert.Equal(t, "https://workiq.svc.cloud.microsoft/rest", Registration.Discovery.DefaultBaseURL)
}

func TestChatCompletion_CreatesAConversationThenSendsThePrompt(t *testing.T) {
	provider, capture := copilotServer(t, jsonHandler(http.StatusOK, chatConversation), nil)
	temperature := 0.2

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:       defaultModel,
		Temperature: &temperature,
		Messages: []core.Message{
			{Role: "system", Content: "Answer briefly."},
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi, how can I help?"},
			{Role: "user", Content: prompt},
		},
	})
	require.NoError(t, err)

	requests := capture.All()
	require.Len(t, requests, 2)

	create := requests[0]
	assert.Equal(t, http.MethodPost, create.Method)
	assert.Equal(t, "/conversations", create.Path)
	assert.JSONEq(t, `{}`, string(create.Body))
	assert.Equal(t, "Bearer entra-token", create.Header.Get("Authorization"))

	chat := requests[1]
	assert.Equal(t, http.MethodPost, chat.Method)
	assert.Equal(t, chatPath, chat.Path)
	assert.Equal(t, "Bearer entra-token", chat.Header.Get("Authorization"))
	assert.JSONEq(t, `{
		"message": {"text": "`+prompt+`"},
		"additionalContext": [
			{"description": "System instructions", "text": "Answer briefly."},
			{"description": "Earlier user message", "text": "Hello"},
			{"description": "Earlier assistant reply", "text": "Hi, how can I help?"}
		],
		"locationHint": {"timeZone": "`+provider.timeZone+`"}
	}`, string(chat.Body))

	assert.Equal(t, "chatcmpl-"+conversationID, resp.ID)
	assert.Equal(t, "chat.completion", resp.Object)
	assert.Equal(t, defaultModel, resp.Model)
	require.Len(t, resp.Choices, 1)
	assert.Equal(t, "assistant", resp.Choices[0].Message.Role)
	assert.Equal(t, reply, resp.Choices[0].Message.Content)
	assert.Equal(t, "stop", resp.Choices[0].FinishReason)
	assert.Zero(t, resp.Usage.TotalTokens)
}

func TestChatCompletion_DoesNotRetryThePrompt(t *testing.T) {
	provider, capture := copilotServer(t, jsonHandler(http.StatusBadGateway, `{"error":{"message":"upstream timeout"}}`), nil)

	_, err := provider.ChatCompletion(context.Background(), userRequest(prompt))
	require.Error(t, err)
	// One conversation and one prompt: a retry could add a second turn.
	assert.Equal(t, 2, capture.Count())
}

func TestChatCompletion_UsesOneTokenForBothCalls(t *testing.T) {
	srv, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/conversations": jsonHandler(http.StatusCreated, createdConversation),
		chatPath:         jsonHandler(http.StatusOK, chatConversation),
	})
	opts := providertest.Options(llmclient.Hooks{})
	opts.HTTPClient = srv.Client()
	opts.Keys = providers.NewKeyring("token-a", "token-b")
	provider := New(providers.ProviderConfig{BaseURL: srv.URL}, opts)

	for range 2 {
		_, err := provider.ChatCompletion(context.Background(), userRequest(prompt))
		require.NoError(t, err)
	}

	requests := capture.All()
	require.Len(t, requests, 4)
	// A conversation belongs to the user who created it, so each request
	// keeps its token while the keyring rotates between requests.
	assert.Equal(t, requests[0].Header.Get("Authorization"), requests[1].Header.Get("Authorization"))
	assert.Equal(t, requests[2].Header.Get("Authorization"), requests[3].Header.Get("Authorization"))
	assert.NotEqual(t, requests[0].Header.Get("Authorization"), requests[2].Header.Get("Authorization"))
}

func TestNewChatBody(t *testing.T) {
	tests := []struct {
		name string
		req  *core.ChatRequest
		want string
	}{
		{
			name: "a lone user message is the prompt",
			req:  userRequest("Hello"),
			want: `{"message":{"text":"Hello"},"locationHint":{"timeZone":"Europe/Warsaw"}}`,
		},
		{
			name: "text parts are joined and a developer message is system context",
			req: &core.ChatRequest{Messages: []core.Message{
				{Role: "developer", Content: "Be terse."},
				{Role: "user", Content: []core.ContentPart{{Type: "text", Text: "First."}, {Type: "text", Text: "Second."}}},
			}},
			want: `{
				"message": {"text": "First.\nSecond."},
				"additionalContext": [{"description": "System instructions", "text": "Be terse."}],
				"locationHint": {"timeZone": "Europe/Warsaw"}
			}`,
		},
		{
			name: "an earlier message without text adds no context",
			req: &core.ChatRequest{Messages: []core.Message{
				{Role: "assistant", Content: nil},
				{Role: "user", Content: "Hello"},
			}},
			want: `{"message":{"text":"Hello"},"locationHint":{"timeZone":"Europe/Warsaw"}}`,
		},
		{
			name: "tools the caller switched off are dropped",
			req: &core.ChatRequest{
				Messages:   []core.Message{{Role: "user", Content: "Hello"}},
				Tools:      []map[string]any{{"type": "function"}},
				ToolChoice: "none",
			},
			want: `{"message":{"text":"Hello"},"locationHint":{"timeZone":"Europe/Warsaw"}}`,
		},
		{
			name: "Chat API parameters pass through as extra fields",
			req: &core.ChatRequest{
				Messages: []core.Message{{Role: "user", Content: "Summarize this document."}},
				ExtraFields: extraFields(t, map[string]string{
					"locationHint":        `{"timeZone":"America/New_York"}`,
					"contextualResources": `{"files":[{"uri":"https://contoso.sharepoint.com/a.docx"}],"webContext":{"isWebEnabled":false}}`,
					"response_format":     `{"type":"text"}`,
					"n":                   `1`,
				}),
			},
			want: `{
				"message": {"text": "Summarize this document."},
				"locationHint": {"timeZone": "America/New_York"},
				"contextualResources": {"files": [{"uri": "https://contoso.sharepoint.com/a.docx"}], "webContext": {"isWebEnabled": false}}
			}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := newChatBody(tt.req, "Europe/Warsaw")
			require.NoError(t, err)
			got, err := json.Marshal(body)
			require.NoError(t, err)
			assert.JSONEq(t, tt.want, string(got))
		})
	}
}

func TestChatCompletion_RejectsWhatCopilotCannotServe(t *testing.T) {
	tests := []struct {
		name    string
		req     *core.ChatRequest
		message string
	}{
		{
			name: "tools",
			req: &core.ChatRequest{
				Messages: []core.Message{{Role: "user", Content: "Hello"}},
				Tools:    []map[string]any{{"type": "function"}},
			},
			message: "tools are not supported",
		},
		{
			name: "a tool call in the history",
			req: &core.ChatRequest{Messages: []core.Message{
				{Role: "assistant", ToolCalls: []core.ToolCall{{ID: "call_1"}}},
				{Role: "user", Content: "Hello"},
			}},
			message: "messages[0]: tool calls are not supported",
		},
		{
			name: "a tool result",
			req: &core.ChatRequest{Messages: []core.Message{
				{Role: "tool", ToolCallID: "call_1", Content: "42"},
				{Role: "user", Content: "Hello"},
			}},
			message: "messages[0]: tool results are not supported",
		},
		{
			name: "an image",
			req: &core.ChatRequest{Messages: []core.Message{{Role: "user", Content: []core.ContentPart{
				{Type: "text", Text: "What is this?"},
				{Type: "image_url", ImageURL: &core.ImageURLContent{URL: "https://example.com/a.png"}},
			}}}},
			message: "messages[0]: image_url content is not supported",
		},
		{
			name:    "several choices",
			req:     &core.ChatRequest{Messages: []core.Message{{Role: "user", Content: "Hello"}}, ExtraFields: extraFields(t, map[string]string{"n": `2`})},
			message: "n > 1 is not supported",
		},
		{
			name:    "JSON output",
			req:     &core.ChatRequest{Messages: []core.Message{{Role: "user", Content: "Hello"}}, ExtraFields: extraFields(t, map[string]string{"response_format": `{"type":"json_object"}`})},
			message: "response_format json_object is not supported",
		},
		{
			name:    "audio output",
			req:     &core.ChatRequest{Messages: []core.Message{{Role: "user", Content: "Hello"}}, ExtraFields: extraFields(t, map[string]string{"modalities": `["text","audio"]`})},
			message: "modality audio is not supported",
		},
		{
			name:    "no messages",
			req:     &core.ChatRequest{},
			message: "messages must not be empty",
		},
		{
			name: "a last message that is not the user's",
			req: &core.ChatRequest{Messages: []core.Message{
				{Role: "user", Content: "Hello"},
				{Role: "assistant", Content: "Hi"},
			}},
			message: "the last message must be a user message with text",
		},
		{
			name:    "a prompt without text",
			req:     userRequest("  "),
			message: "the last message must be a user message with text",
		},
		{
			name:    "an unknown role",
			req:     &core.ChatRequest{Messages: []core.Message{{Role: "narrator", Content: "Hello"}}},
			message: `messages[0]: role "narrator" is not supported`,
		},
		{
			name:    "a location hint that is not an object",
			req:     &core.ChatRequest{Messages: []core.Message{{Role: "user", Content: "Hello"}}, ExtraFields: extraFields(t, map[string]string{"locationHint": `"Europe/Warsaw"`})},
			message: "locationHint must be a JSON object",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, capture := copilotServer(t, jsonHandler(http.StatusOK, chatConversation), sseHandler(""))

			_, err := provider.ChatCompletion(context.Background(), tt.req)
			assertInvalidRequest(t, err, tt.message)
			_, err = provider.StreamChatCompletion(context.Background(), tt.req)
			assertInvalidRequest(t, err, tt.message)

			assert.Zero(t, capture.Count(), "a rejected request must not open a conversation")
		})
	}
}

func assertInvalidRequest(t *testing.T, err error, message string) {
	t.Helper()
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
	assert.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
	assert.Contains(t, gatewayErr.Message, message)
}

func TestChatCompletion_ContentChecksEndTheConversation(t *testing.T) {
	provider, _ := copilotServer(t, jsonHandler(http.StatusOK,
		`{"id":"`+conversationID+`","state":"disengagedForRai","messages":[{"text":"Hello"}]}`), nil)

	resp, err := provider.ChatCompletion(context.Background(), userRequest("Hello"))
	require.NoError(t, err)
	require.Len(t, resp.Choices, 1)
	assert.Equal(t, "content_filter", resp.Choices[0].FinishReason)
	assert.Empty(t, resp.Choices[0].Message.Content)
}

func TestChatCompletion_NoReplyIsAProviderError(t *testing.T) {
	provider, _ := copilotServer(t, jsonHandler(http.StatusOK,
		`{"id":"`+conversationID+`","state":"active","messages":[{"text":"Hello"}]}`), nil)

	_, err := provider.ChatCompletion(context.Background(), userRequest("Hello"))
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusBadGateway, gatewayErr.HTTPStatusCode())
}

func TestChatCompletion_RelaysUpstreamErrors(t *testing.T) {
	const upstreamError = `{"error":{"code":"InvalidAuthenticationToken","message":"Lifetime validation failed, the token is expired.","innerError":{"request-id":"r1"}}}`
	tests := []struct {
		name   string
		routes map[string]http.HandlerFunc
		calls  int
	}{
		{
			name:   "creating the conversation",
			routes: map[string]http.HandlerFunc{"/conversations": jsonHandler(http.StatusUnauthorized, upstreamError)},
			calls:  1,
		},
		{
			name: "sending the prompt",
			routes: map[string]http.HandlerFunc{
				"/conversations": jsonHandler(http.StatusCreated, createdConversation),
				chatPath:         jsonHandler(http.StatusUnauthorized, upstreamError),
			},
			calls: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, capture := providertest.RouteServer(t, tt.routes)
			provider := newTestProvider("entra-token", srv.URL, srv.Client(), llmclient.Hooks{})

			_, err := provider.ChatCompletion(context.Background(), userRequest("Hello"))
			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, http.StatusUnauthorized, gatewayErr.HTTPStatusCode())
			assert.Contains(t, gatewayErr.Message, "the token is expired")
			assert.NotContains(t, gatewayErr.Error(), "entra-token")
			assert.Equal(t, tt.calls, capture.Count())
		})
	}
}

func TestChatCompletion_CreatedConversationWithoutAnID(t *testing.T) {
	srv, _ := providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/conversations": jsonHandler(http.StatusCreated, `{}`),
	})
	provider := newTestProvider("entra-token", srv.URL, srv.Client(), llmclient.Hooks{})

	_, err := provider.ChatCompletion(context.Background(), userRequest("Hello"))
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusBadGateway, gatewayErr.HTTPStatusCode())
	assert.Contains(t, gatewayErr.Message, "no conversation ID")
}

func TestChatCompletion_MissingToken(t *testing.T) {
	provider, capture := copilotServer(t, jsonHandler(http.StatusOK, chatConversation), nil)
	provider.keys = nil

	_, err := provider.ChatCompletion(context.Background(), userRequest("Hello"))
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusUnauthorized, gatewayErr.HTTPStatusCode())
	assert.Contains(t, gatewayErr.Message, "M365_COPILOT_API_KEY")
	assert.Zero(t, capture.Count())
}

// snapshot renders one chatOverStream event: a conversation whose messages
// hold the given texts.
func snapshot(t *testing.T, state string, texts ...string) string {
	t.Helper()
	messages := make([]map[string]any, 0, len(texts))
	for _, text := range texts {
		messages = append(messages, map[string]any{"text": text, "adaptiveCards": []any{}})
	}
	body, err := json.Marshal(map[string]any{
		"id":          conversationID,
		"displayName": "Intermediate Conversation Update",
		"state":       state,
		"messages":    messages,
	})
	require.NoError(t, err)
	return string(body)
}

// chunk is the part of a chat completion chunk the stream tests read.
type chunk struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *core.Usage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// readChunks drains a chat completion stream into its chunks and reports
// whether it ended with [DONE].
func readChunks(t *testing.T, stream io.ReadCloser) (chunks []chunk, done bool) {
	t.Helper()
	defer func() { _ = stream.Close() }()
	raw, err := io.ReadAll(stream)
	require.NoError(t, err)
	for event := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n\n") {
		data := strings.TrimPrefix(event, "data: ")
		if data == "[DONE]" {
			done = true
			continue
		}
		require.False(t, done, "no chunk may follow [DONE]")
		var c chunk
		require.NoError(t, json.Unmarshal([]byte(data), &c), "chunk %q", data)
		chunks = append(chunks, c)
	}
	return chunks, done
}

// streamedText joins the content deltas and returns the finish reason.
func streamedText(t *testing.T, chunks []chunk) (text, finish string) {
	t.Helper()
	var b strings.Builder
	for _, c := range chunks {
		require.Nil(t, c.Error, "unexpected error chunk")
		require.Len(t, c.Choices, 1)
		b.WriteString(c.Choices[0].Delta.Content)
		if c.Choices[0].FinishReason != nil {
			finish = *c.Choices[0].FinishReason
		}
	}
	return b.String(), finish
}

func TestStreamChatCompletion_ForwardsWhatEachSnapshotAdds(t *testing.T) {
	// The upstream repeats the whole answer in every event, then closes with
	// the conversation as the chat action returns it: prompt echo first.
	events := []string{
		snapshot(t, "active", prompt),
		snapshot(t, "active", "You have"),
		snapshot(t, "active", "You have"),
		snapshot(t, "active", "You have **1 meeting**"),
		snapshot(t, "active"),
		snapshot(t, "active", prompt, reply),
	}
	// indented is the event as the reference prints it: JSON over several
	// lines with "data:" on the first one only.
	indented := func(event string) string {
		var v any
		require.NoError(t, json.Unmarshal([]byte(event), &v))
		pretty, err := json.MarshalIndent(v, "", "  ")
		require.NoError(t, err)
		return string(pretty)
	}
	tests := []struct {
		name string
		body func() string
	}{
		{
			name: "one line per event",
			body: func() string {
				var b strings.Builder
				for i, event := range events {
					b.WriteString("data: " + event + "\nid:" + string(rune('1'+i)) + "\n\n")
				}
				return b.String()
			},
		},
		{
			name: "indented as in the reference, CRLF, no trailing blank line",
			body: func() string {
				var b strings.Builder
				for i, event := range events {
					b.WriteString("data: " + strings.ReplaceAll(indented(event), "\n", "\r\n") + "\r\nid:" + string(rune('1'+i)))
					if i < len(events)-1 {
						b.WriteString("\r\n\r\n")
					}
				}
				return b.String()
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, capture := copilotServer(t, nil, sseHandler(tt.body()))

			stream, err := provider.StreamChatCompletion(context.Background(), userRequest(prompt))
			require.NoError(t, err)
			chunks, done := readChunks(t, stream)

			assert.True(t, done)
			require.Len(t, chunks, 4)
			assert.Equal(t, "assistant", chunks[0].Choices[0].Delta.Role)
			assert.Equal(t, "You have", chunks[0].Choices[0].Delta.Content)
			assert.Equal(t, " **1 meeting**", chunks[1].Choices[0].Delta.Content)
			assert.Empty(t, chunks[1].Choices[0].Delta.Role)
			text, finish := streamedText(t, chunks)
			assert.Equal(t, reply, text)
			assert.Equal(t, "stop", finish)
			for _, c := range chunks {
				assert.Equal(t, "chatcmpl-"+conversationID, c.ID)
				assert.Equal(t, "chat.completion.chunk", c.Object)
				assert.Equal(t, defaultModel, c.Model)
				assert.Nil(t, c.Usage)
			}

			sent := capture.Last(t)
			assert.Equal(t, streamPath, sent.Path)
			assert.Equal(t, "Bearer entra-token", sent.Header.Get("Authorization"))
			assert.Equal(t, prompt, sent.JSON(t)["message"].(map[string]any)["text"])
		})
	}
}

func TestStreamChatCompletion_ReportsZeroUsageWhenAsked(t *testing.T) {
	provider, _ := copilotServer(t, nil, sseHandler("data: "+snapshot(t, "active", "Hi")+"\n\n"))
	req := userRequest("Hello")
	req.StreamOptions = &core.StreamOptions{IncludeUsage: true}

	stream, err := provider.StreamChatCompletion(context.Background(), req)
	require.NoError(t, err)
	chunks, done := readChunks(t, stream)

	assert.True(t, done)
	require.Len(t, chunks, 2)
	require.NotNil(t, chunks[1].Usage)
	assert.Zero(t, chunks[1].Usage.TotalTokens)
}

func TestStreamChatCompletion_SkipsASnapshotThatRewritesSentText(t *testing.T) {
	provider, _ := copilotServer(t, nil, sseHandler(
		"data: "+snapshot(t, "active", "The answer is 4")+"\n\n"+
			"data: "+snapshot(t, "active", "Answer: 4")+"\n\n"+
			"data: "+snapshot(t, "active", "The answer is 42.")+"\n\n"))

	stream, err := provider.StreamChatCompletion(context.Background(), userRequest("Hello"))
	require.NoError(t, err)
	chunks, _ := readChunks(t, stream)

	text, finish := streamedText(t, chunks)
	assert.Equal(t, "The answer is 42.", text)
	assert.Equal(t, "stop", finish)
}

func TestStreamChatCompletion_ContentChecksEndTheConversation(t *testing.T) {
	provider, _ := copilotServer(t, nil, sseHandler("data: "+snapshot(t, "disengagedForRai", "Hello")+"\n\n"))

	stream, err := provider.StreamChatCompletion(context.Background(), userRequest("Hello"))
	require.NoError(t, err)
	chunks, done := readChunks(t, stream)

	assert.True(t, done)
	text, finish := streamedText(t, chunks)
	assert.Empty(t, text)
	assert.Equal(t, "content_filter", finish)
}

func TestStreamChatCompletion_FailuresBecomeAnErrorChunk(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		message string
	}{
		{
			name:    "an error event after some text",
			body:    "data: " + snapshot(t, "active", "Hi") + "\n\n" + `data: {"error":{"code":"generalException","message":"Copilot is unavailable."}}` + "\n\n",
			message: "Copilot is unavailable.",
		},
		{
			name:    "an event that is not JSON",
			body:    "data: " + snapshot(t, "active", "Hi") + "\n\ndata: {not json\n\n",
			message: "failed to parse a Microsoft 365 Copilot stream event",
		},
		{
			name: "a final answer that rewrites streamed text",
			body: "data: " + snapshot(t, "active", "The answer is 4") + "\n\n" +
				"data: " + snapshot(t, "active", "The answer is 5") + "\n\n",
			message: "revised text it had already streamed",
		},
		{
			name:    "an event over the size limit",
			body:    "data: {\n" + strings.Repeat(strings.Repeat("x", 1<<20)+"\n", 9) + "\n",
			message: "exceeds the size limit",
		},
		{
			name:    "no answer before the stream ends",
			body:    "data: " + snapshot(t, "active", "Hello") + "\n\n",
			message: "stream ended without an answer",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, _ := copilotServer(t, nil, sseHandler(tt.body))

			stream, err := provider.StreamChatCompletion(context.Background(), userRequest("Hello"))
			require.NoError(t, err)
			chunks, done := readChunks(t, stream)

			assert.True(t, done)
			require.NotEmpty(t, chunks)
			last := chunks[len(chunks)-1]
			require.NotNil(t, last.Error)
			assert.Equal(t, string(core.ErrorTypeProvider), last.Error.Type)
			assert.Contains(t, last.Error.Message, tt.message)
		})
	}
}

func TestStreamChatCompletion_RelaysUpstreamErrors(t *testing.T) {
	provider, _ := copilotServer(t, nil, jsonHandler(http.StatusForbidden,
		`{"error":{"code":"Forbidden","message":"Work IQ is not enabled for this organization."}}`))

	_, err := provider.StreamChatCompletion(context.Background(), userRequest("Hello"))
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusForbidden, gatewayErr.HTTPStatusCode())
	assert.Contains(t, gatewayErr.Message, "Work IQ is not enabled")
}

func TestResponses_GoThroughChat(t *testing.T) {
	provider, capture := copilotServer(t, jsonHandler(http.StatusOK, chatConversation), nil)

	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model:        defaultModel,
		Instructions: "Answer briefly.",
		Input:        prompt,
	})
	require.NoError(t, err)

	sent := capture.Last(t).JSON(t)
	assert.Equal(t, map[string]any{"text": prompt}, sent["message"])
	assert.Equal(t, []any{map[string]any{"description": "System instructions", "text": "Answer briefly."}}, sent["additionalContext"])
	require.NotEmpty(t, resp.Output)
	body, err := json.Marshal(resp.Output)
	require.NoError(t, err)
	assert.Contains(t, string(body), "Contoso Engineering Standup")
}

func TestListModels(t *testing.T) {
	tests := []struct {
		name       string
		configured []string
		want       []string
	}{
		{name: "the default model", want: []string{"m365-copilot"}},
		{name: "configured names", configured: []string{"copilot", "work-chat"}, want: []string{"copilot", "work-chat"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := providertest.Options(llmclient.Hooks{})
			opts.Models = tt.configured
			provider := New(providers.ProviderConfig{APIKey: "entra-token"}, opts)

			resp, err := provider.ListModels(context.Background())
			require.NoError(t, err)

			ids := make([]string, 0, len(resp.Data))
			for _, model := range resp.Data {
				ids = append(ids, model.ID)
				assert.Equal(t, "microsoft", model.OwnedBy)
				require.NotNil(t, model.Metadata)
				assert.Equal(t, []string{"chat"}, model.Metadata.Modes)
			}
			assert.Equal(t, tt.want, ids)
		})
	}
}

func TestEmbeddings_AreUnsupported(t *testing.T) {
	provider, capture := copilotServer(t, nil, nil)

	_, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{Model: defaultModel})
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusNotImplemented, gatewayErr.HTTPStatusCode())
	assert.Zero(t, capture.Count())
}

func TestIANATimeZone(t *testing.T) {
	warsaw, err := time.LoadLocation("Europe/Warsaw")
	require.NoError(t, err)
	tests := []struct {
		name string
		loc  *time.Location
		want string
	}{
		{name: "a named zone", loc: warsaw, want: "Europe/Warsaw"},
		{name: "UTC", loc: time.UTC, want: "UTC"},
		{name: "the system zone has no IANA name", loc: time.FixedZone("Local", 3600), want: "UTC"},
		{name: "a zone file path", loc: time.FixedZone("/etc/localtime", 3600), want: "UTC"},
		{name: "an unnamed zone", loc: time.FixedZone("", 3600), want: "UTC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ianaTimeZone(tt.loc))
		})
	}
}

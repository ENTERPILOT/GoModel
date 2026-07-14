package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func conversationTestProvider(t *testing.T) *capturingProvider {
	t.Helper()
	return &capturingProvider{mockProvider: mockProvider{
		supportedModels: []string{"gpt-5-mini"},
		providerTypes:   map[string]string{"gpt-5-mini": "mock"},
		responsesResponse: &core.ResponsesResponse{
			ID:     "resp_conv_1",
			Object: "response",
			Model:  "gpt-5-mini",
			Status: "completed",
			Output: []core.ResponsesOutputItem{
				{
					ID:   "msg_out_1",
					Type: "message",
					Role: "assistant",
					Content: []core.ResponsesContentItem{
						{Type: "output_text", Text: "the word is zebra"},
					},
				},
			},
		},
	}}
}

func createTestConversation(t *testing.T, srv http.Handler, body string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/conversations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create conversation status = %d (%s)", rec.Code, rec.Body.String())
	}
	var conv core.Conversation
	if err := json.Unmarshal(rec.Body.Bytes(), &conv); err != nil {
		t.Fatalf("decode conversation: %v", err)
	}
	return conv.ID
}

func postResponses(t *testing.T, srv http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestResponsesWithConversation_ResolvesLocallyAndAppendsTurn(t *testing.T) {
	provider := conversationTestProvider(t)
	srv := New(provider, nil)

	convID := createTestConversation(t, srv,
		`{"items":[{"type":"message","role":"user","content":[{"type":"input_text","text":"remember: zebra"}]}]}`)

	rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"what is the word?","conversation":"`+convID+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("responses status = %d (%s)", rec.Code, rec.Body.String())
	}

	forwarded := provider.capturedResponsesReq
	if forwarded == nil {
		t.Fatal("provider did not receive a responses request")
	}
	if forwarded.Conversation != nil {
		t.Fatalf("conversation field must be stripped before dispatch, got %+v", forwarded.Conversation)
	}
	input, ok := forwarded.Input.([]any)
	if !ok || len(input) != 2 {
		t.Fatalf("forwarded input = %#v, want history + user message (2 items)", forwarded.Input)
	}
	history, ok := input[0].(map[string]any)
	if !ok || history["role"] != "user" {
		t.Fatalf("first forwarded item = %#v, want stored history item", input[0])
	}
	if _, hasID := history["id"]; hasID {
		t.Fatalf("stored item id must be stripped before dispatch, got %#v", history)
	}

	// Second turn: the conversation now holds initial item + turn input + output.
	rec = postResponses(t, srv, `{"model":"gpt-5-mini","input":"and again?","conversation":"`+convID+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("second responses status = %d (%s)", rec.Code, rec.Body.String())
	}
	input, ok = provider.capturedResponsesReq.Input.([]any)
	if !ok || len(input) != 4 {
		t.Fatalf("second turn forwarded %d items, want 4 (3 history + 1 new input): %#v", len(input), provider.capturedResponsesReq.Input)
	}
	assistant, ok := input[2].(map[string]any)
	if !ok || assistant["role"] != "assistant" {
		t.Fatalf("third forwarded item = %#v, want appended assistant output", input[2])
	}
}

func TestResponsesWithConversation_UnknownIDReturns404(t *testing.T) {
	provider := conversationTestProvider(t)
	srv := New(provider, nil)

	rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"hello","conversation":"conv_missing"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d (%s), want 404", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Conversation with id 'conv_missing' not found") {
		t.Fatalf("body = %s, want conversation not found message", rec.Body.String())
	}
	if provider.capturedResponsesReq != nil {
		t.Fatal("provider must not be called for an unknown conversation")
	}
}

func TestResponsesWithConversation_RejectsPreviousResponseID(t *testing.T) {
	provider := conversationTestProvider(t)
	srv := New(provider, nil)
	convID := createTestConversation(t, srv, `{}`)

	rec := postResponses(t, srv,
		`{"model":"gpt-5-mini","input":"hello","conversation":"`+convID+`","previous_response_id":"resp_1"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d (%s), want 400", rec.Code, rec.Body.String())
	}
}

func TestResponsesWithConversation_ObjectRefAndStringInputShapes(t *testing.T) {
	provider := conversationTestProvider(t)
	srv := New(provider, nil)
	convID := createTestConversation(t, srv, `{}`)

	rec := postResponses(t, srv,
		`{"model":"gpt-5-mini","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"conversation":{"id":"`+convID+`"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	input, ok := provider.capturedResponsesReq.Input.([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("forwarded input = %#v, want the single request item (empty history)", provider.capturedResponsesReq.Input)
	}
}

func TestResponsesWithConversation_StreamingAppendsTurn(t *testing.T) {
	streamData := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_s1"}}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_s1","output":[{"id":"msg_s1","type":"message","role":"assistant","content":[{"type":"output_text","text":"streamed answer"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	provider := conversationTestProvider(t)
	provider.streamData = streamData
	srv := New(provider, nil)
	convID := createTestConversation(t, srv, `{}`)

	rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"start","conversation":"`+convID+`","stream":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("stream status = %d (%s)", rec.Code, rec.Body.String())
	}

	// The streamed exchange (input + completed output) must now be history.
	rec = postResponses(t, srv, `{"model":"gpt-5-mini","input":"next","conversation":"`+convID+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("follow-up status = %d (%s)", rec.Code, rec.Body.String())
	}
	input, ok := provider.capturedResponsesReq.Input.([]any)
	if !ok || len(input) != 3 {
		t.Fatalf("follow-up forwarded %d items, want 3 (streamed input + output + new input): %#v", len(input), provider.capturedResponsesReq.Input)
	}
	assistant, ok := input[1].(map[string]any)
	if !ok || assistant["role"] != "assistant" {
		t.Fatalf("second item = %#v, want streamed assistant output", input[1])
	}
}

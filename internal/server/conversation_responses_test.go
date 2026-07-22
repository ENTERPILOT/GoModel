package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/conversationstore"
	"github.com/enterpilot/gomodel/internal/core"
)

type appendFailingConversationStore struct {
	*conversationstore.MemoryStore
	err error
}

func (s *appendFailingConversationStore) AppendItems(context.Context, string, []json.RawMessage) error {
	return s.err
}

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

func TestResponsesWithConversation_StreamingAppendFailureSuppressesCompletion(t *testing.T) {
	provider := conversationTestProvider(t)
	provider.streamData = strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_failed_persist"}}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_failed_persist","output":[{"id":"msg_failed_persist","type":"message","role":"assistant","content":[{"type":"output_text","text":"not saved"}]}]}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	store := &appendFailingConversationStore{
		MemoryStore: conversationstore.NewMemoryStore(),
		err:         errors.New("append unavailable"),
	}
	srv := New(provider, &Config{ConversationStore: store})
	convID := createTestConversation(t, srv, `{}`)

	rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"hello","conversation":"`+convID+`","stream":true}`)
	if strings.Contains(rec.Body.String(), "response.completed") {
		t.Fatalf("stream body = %s, must not report completion when persistence fails", rec.Body.String())
	}
}

func TestResponsesWithConversation_PreservesReasoningFieldsOnReplay(t *testing.T) {
	provider := conversationTestProvider(t)
	var response core.ResponsesResponse
	if err := json.Unmarshal([]byte(`{
		"id":"resp_reasoning","object":"response","model":"gpt-5-mini","status":"completed",
		"output":[{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"opaque"}]
	}`), &response); err != nil {
		t.Fatalf("decode reasoning response: %v", err)
	}
	provider.responsesResponse = &response
	srv := New(provider, nil)
	convID := createTestConversation(t, srv, `{}`)

	if rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"first","conversation":"`+convID+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("first response status = %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"second","conversation":"`+convID+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("second response status = %d (%s)", rec.Code, rec.Body.String())
	}
	input, ok := provider.capturedResponsesReq.Input.([]any)
	if !ok || len(input) != 3 {
		t.Fatalf("replayed input = %#v, want first + reasoning + second", provider.capturedResponsesReq.Input)
	}
	reasoning, ok := input[1].(map[string]any)
	if !ok || reasoning["type"] != "reasoning" || reasoning["encrypted_content"] != "opaque" {
		t.Fatalf("reasoning item = %#v, want lossless replay", input[1])
	}
	if _, ok := reasoning["summary"].([]any); !ok {
		t.Fatalf("reasoning summary = %#v, want array", reasoning["summary"])
	}
}

func TestMergeConversationInputPreservesLargeUnknownIntegers(t *testing.T) {
	merged, err := mergeConversationInput([]json.RawMessage{
		json.RawMessage(`{"id":"future_1","type":"future_item","opaque_integer":9007199254740993}`),
	}, nil)
	if err != nil {
		t.Fatalf("merge conversation input: %v", err)
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		t.Fatalf("marshal merged input: %v", err)
	}
	if !strings.Contains(string(encoded), `"opaque_integer":9007199254740993`) {
		t.Fatalf("merged input = %s, want exact large integer", encoded)
	}
}

func TestResponsesWithConversation_RemapsReusedProviderItemIDs(t *testing.T) {
	provider := conversationTestProvider(t)
	srv := New(provider, nil)
	convID := createTestConversation(t, srv, `{}`)

	returnedOutputIDs := make(map[string]struct{}, 3)
	for _, input := range []string{"first", "second", "third"} {
		rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"`+input+`","conversation":"`+convID+`"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("response for %q status = %d (%s)", input, rec.Code, rec.Body.String())
		}
		var response core.ResponsesResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || len(response.Output) != 1 {
			t.Fatalf("decode response for %q: output=%v err=%v", input, response.Output, err)
		}
		returnedOutputIDs[response.Output[0].ID] = struct{}{}
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+convID+"/items?order=asc&limit=100", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d (%s)", rec.Code, rec.Body.String())
	}
	var list core.ConversationItemListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode item list: %v", err)
	}
	if len(list.Data) != 6 {
		t.Fatalf("items = %d, want three inputs and three outputs", len(list.Data))
	}
	ids := make(map[string]struct{}, len(list.Data))
	for _, raw := range list.Data {
		id := responseInputItemID(raw)
		if id == "" {
			t.Fatalf("item has no id: %s", raw)
		}
		if _, duplicate := ids[id]; duplicate {
			t.Fatalf("duplicate persisted item id %q in %s", id, rec.Body.String())
		}
		ids[id] = struct{}{}
	}
	for id := range returnedOutputIDs {
		if _, persisted := ids[id]; !persisted {
			t.Fatalf("response output id %q was not persisted: %s", id, rec.Body.String())
		}
		itemReq := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+convID+"/items/"+id, nil)
		itemRec := httptest.NewRecorder()
		srv.ServeHTTP(itemRec, itemReq)
		if itemRec.Code != http.StatusOK {
			t.Fatalf("retrieve returned output id %q status = %d (%s)", id, itemRec.Code, itemRec.Body.String())
		}
	}
}

func TestResponsesWithConversation_GeneratesMissingProviderOutputID(t *testing.T) {
	provider := conversationTestProvider(t)
	provider.responsesResponse.Output[0].ID = ""
	srv := New(provider, nil)
	convID := createTestConversation(t, srv, `{}`)

	rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"hello","conversation":"`+convID+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("response status = %d (%s)", rec.Code, rec.Body.String())
	}
	var response core.ResponsesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || len(response.Output) != 1 {
		t.Fatalf("decode response: output=%v err=%v", response.Output, err)
	}
	if response.Output[0].ID == "" {
		t.Fatal("response output id is empty, want gateway-generated id")
	}
	itemReq := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+convID+"/items/"+response.Output[0].ID, nil)
	itemRec := httptest.NewRecorder()
	srv.ServeHTTP(itemRec, itemReq)
	if itemRec.Code != http.StatusOK {
		t.Fatalf("retrieve generated output id status = %d (%s)", itemRec.Code, itemRec.Body.String())
	}
}

func TestResponsesWithConversation_AppendFailureReturnsError(t *testing.T) {
	provider := conversationTestProvider(t)
	store := &appendFailingConversationStore{
		MemoryStore: conversationstore.NewMemoryStore(),
		err:         errors.New("append unavailable"),
	}
	srv := New(provider, &Config{ConversationStore: store})
	convID := createTestConversation(t, srv, `{}`)

	rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"hello","conversation":"`+convID+`"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("response status = %d (%s), want 500", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "failed to append conversation turn") {
		t.Fatalf("response body = %s, want append failure", rec.Body.String())
	}
}

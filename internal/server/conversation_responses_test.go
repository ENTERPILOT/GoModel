package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/conversationstore"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
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
	return &capturingProvider{
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
		}}
}

func createTestConversation(t *testing.T, srv http.Handler, body string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/conversations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	conv := echotest.Decode[core.Conversation](t, rec)

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
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	forwarded := provider.capturedResponsesReq
	require.NotNil(t, forwarded)
	require.Nil(t, forwarded.Conversation)

	input, ok := forwarded.Input.([]any)
	require.True(t, ok)
	require.Len(t, input, 2)

	history, ok := input[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "user", history["role"], "first forwarded item = %#v, want stored history item", input[0])
	_, hasID := history["id"]
	require.False(t, hasID, "stored item id must be stripped before dispatch, got %#v", history)

	// Second turn: the conversation now holds initial item + turn input + output.
	rec = postResponses(t, srv, `{"model":"gpt-5-mini","input":"and again?","conversation":"`+convID+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	input, ok = provider.capturedResponsesReq.Input.([]any)
	require.True(t, ok)
	require.Len(t, input, 4)

	assistant, ok := input[2].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "assistant", assistant["role"], "third forwarded item = %#v, want appended assistant output", input[2])
}

func TestResponsesWithConversation_UnknownIDReturns404(t *testing.T) {
	provider := conversationTestProvider(t)
	srv := New(provider, nil)

	rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"hello","conversation":"conv_missing"}`)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "Conversation with id 'conv_missing' not found")
	require.Nil(t, provider.capturedResponsesReq)
}

func TestResponsesWithConversation_RejectsPreviousResponseID(t *testing.T) {
	provider := conversationTestProvider(t)
	srv := New(provider, nil)
	convID := createTestConversation(t, srv, `{}`)

	rec := postResponses(t, srv,
		`{"model":"gpt-5-mini","input":"hello","conversation":"`+convID+`","previous_response_id":"resp_1"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestResponsesWithConversation_ObjectRefAndStringInputShapes(t *testing.T) {
	provider := conversationTestProvider(t)
	srv := New(provider, nil)
	convID := createTestConversation(t, srv, `{}`)

	rec := postResponses(t, srv,
		`{"model":"gpt-5-mini","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"conversation":{"id":"`+convID+`"}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	input, ok := provider.capturedResponsesReq.Input.([]any)
	require.True(t, ok)
	require.Len(t, input, 1)
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
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// The streamed exchange (input + completed output) must now be history.
	rec = postResponses(t, srv, `{"model":"gpt-5-mini","input":"next","conversation":"`+convID+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	input, ok := provider.capturedResponsesReq.Input.([]any)
	require.True(t, ok)
	require.Len(t, input, 3)

	assistant, ok := input[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "assistant", assistant["role"], "second item = %#v, want streamed assistant output", input[1])
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
	require.NotContains(t, rec.Body.String(), "response.completed", "stream body = %s, must not report completion when persistence fails", rec.Body.String())
}

func TestResponsesWithConversation_PreservesReasoningFieldsOnReplay(t *testing.T) {
	provider := conversationTestProvider(t)
	var response core.ResponsesResponse
	err := json.Unmarshal([]byte(`{
		"id":"resp_reasoning","object":"response","model":"gpt-5-mini","status":"completed",
		"output":[{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"opaque"}]
	}`), &response)
	require.NoError(t, err)

	provider.responsesResponse = &response
	srv := New(provider, nil)
	convID := createTestConversation(t, srv, `{}`)
	rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"first","conversation":"`+convID+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = postResponses(t, srv, `{"model":"gpt-5-mini","input":"second","conversation":"`+convID+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	input, ok := provider.capturedResponsesReq.Input.([]any)
	require.True(t, ok)
	require.Len(t, input, 3)

	reasoning, ok := input[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "reasoning", reasoning["type"])
	require.Equal(t, "opaque", reasoning["encrypted_content"], "reasoning item = %#v, want lossless replay", input[1])
	_, ok = reasoning["summary"].([]any)
	require.True(t, ok, "reasoning summary = %#v, want array", reasoning["summary"])
}

func TestMergeConversationInputPreservesLargeUnknownIntegers(t *testing.T) {
	merged, err := mergeConversationInput([]json.RawMessage{
		json.RawMessage(`{"id":"future_1","type":"future_item","opaque_integer":9007199254740993}`),
	}, nil)
	require.NoError(t, err)

	encoded, err := json.Marshal(merged)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"opaque_integer":9007199254740993`, "merged input = %s, want exact large integer", encoded)
}

func TestResponsesWithConversation_RemapsReusedProviderItemIDs(t *testing.T) {
	provider := conversationTestProvider(t)
	srv := New(provider, nil)
	convID := createTestConversation(t, srv, `{}`)

	returnedOutputIDs := make(map[string]struct{}, 3)
	for _, input := range []string{"first", "second", "third"} {
		rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"`+input+`","conversation":"`+convID+`"}`)
		require.Equal(t, http.StatusOK, rec.Code, "response for %q status = %d (%s)", input, rec.Code, rec.Body.String())

		response := echotest.Decode[core.ResponsesResponse](t, rec)
		require.Len(t, response.Output, 1)

		returnedOutputIDs[response.Output[0].ID] = struct{}{}
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+convID+"/items?order=asc&limit=100", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	list := echotest.Decode[core.ConversationItemListResponse](t, rec)
	require.Len(t, list.Data, 6)

	ids := make(map[string]struct{}, len(list.Data))
	for _, raw := range list.Data {
		id := responseInputItemID(raw)
		require.NotEmpty(t, id)
		_, duplicate := ids[id]
		require.False(t, duplicate)

		ids[id] = struct{}{}
	}
	for id := range returnedOutputIDs {
		_, persisted := ids[id]
		require.True(t, persisted)

		itemReq := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+convID+"/items/"+id, nil)
		itemRec := httptest.NewRecorder()
		srv.ServeHTTP(itemRec, itemReq)
		require.Equal(t, http.StatusOK, itemRec.Code, "retrieve returned output id %q status = %d (%s)", id, itemRec.Code, itemRec.Body.String())
	}
}

func TestResponsesWithConversation_GeneratesMissingProviderOutputID(t *testing.T) {
	provider := conversationTestProvider(t)
	provider.responsesResponse.Output[0].ID = ""
	srv := New(provider, nil)
	convID := createTestConversation(t, srv, `{}`)

	rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"hello","conversation":"`+convID+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	response := echotest.Decode[core.ResponsesResponse](t, rec)
	require.Len(t, response.Output, 1)
	require.NotEmpty(t, response.Output[0].ID)

	itemReq := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+convID+"/items/"+response.Output[0].ID, nil)
	itemRec := httptest.NewRecorder()
	srv.ServeHTTP(itemRec, itemReq)
	require.Equal(t, http.StatusOK, itemRec.Code, itemRec.Body.String())
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
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "failed to append conversation turn")
}

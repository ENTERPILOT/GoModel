package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
)

func createConversation(t *testing.T, srv *Server, body string) core.Conversation {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/conversations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	conversation := echotest.Decode[core.Conversation](t, rec)

	return conversation
}

func TestConversationCreateReturnsOpenAICompatibleObject(t *testing.T) {
	srv := New(&mockProvider{}, nil)

	conversation := createConversation(t, srv, `{"metadata":{"topic":"demo"}}`)

	require.True(t, strings.HasPrefix(conversation.ID, "conv_"), "id = %q, want conv_ prefix", conversation.ID)
	require.Equal(t, "conversation", conversation.Object)
	require.Greater(t, conversation.CreatedAt, int64(0))
	require.Equal(t, "demo", conversation.Metadata["topic"])
}

func TestConversationCreateEmptyBodyYieldsEmptyMetadataObject(t *testing.T) {
	srv := New(&mockProvider{}, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/conversations", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	conversation := echotest.Decode[core.Conversation](t, rec)

	// metadata must be an empty object rather than null, matching OpenAI.
	require.NotNil(t, conversation.Metadata)
	require.Empty(t, conversation.Metadata)
}

func TestConversationCreateAcceptsItems(t *testing.T) {
	srv := New(&mockProvider{}, nil)

	conversation := createConversation(t, srv,
		`{"items":[{"type":"message","role":"user","content":"hello"}]}`)
	require.NotEmpty(t, conversation.ID)
}

func TestConversationGetRoundTrip(t *testing.T) {
	srv := New(&mockProvider{}, nil)
	created := createConversation(t, srv, `{"metadata":{"k":"v"}}`)

	req := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+created.ID, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got := echotest.Decode[core.Conversation](t, rec)
	require.Equal(t, created.ID, got.ID)
	require.Equal(t, "v", got.Metadata["k"], "get conversation = %+v, want id %s metadata k=v", got, created.ID)
}

func TestConversationUpdateMergesMetadata(t *testing.T) {
	srv := New(&mockProvider{}, nil)
	created := createConversation(t, srv, `{"metadata":{"old":"value","keep":"gone"}}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/conversations/"+created.ID,
		strings.NewReader(`{"metadata":{"new":"value"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	updated := echotest.Decode[core.Conversation](t, rec)
	require.Equal(t, "value", updated.Metadata["new"])
	require.Equal(t, "value", updated.Metadata["old"])
	require.Equal(t, "gone", updated.Metadata["keep"], "metadata = %v, want existing keys preserved", updated.Metadata)
}

func TestConversationUpdateRejectsOversizedMergedMetadata(t *testing.T) {
	srv := New(&mockProvider{}, nil)
	metadata := make([]string, core.MaxConversationMetadataPairs)
	for index := range metadata {
		metadata[index] = fmt.Sprintf(`"key_%d":"value"`, index)
	}
	created := createConversation(t, srv, `{"metadata":{`+strings.Join(metadata, ",")+`}}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/conversations/"+created.ID,
		strings.NewReader(`{"metadata":{"extra":"value"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	envelope := echotest.Decode[core.OpenAIErrorEnvelope](t, rec)
	require.NotNil(t, envelope.Error.Param)
	require.Equal(t, "metadata", *envelope.Error.Param)
	require.NotNil(t, envelope.Error.Code)
	require.Equal(t, "metadata_max_properties_exceeded", *envelope.Error.Code, "error = %+v, want metadata_max_properties_exceeded", envelope.Error)
}

func TestConversationItemsLifecycleAndPagination(t *testing.T) {
	srv := New(&mockProvider{}, nil)
	created := createConversation(t, srv, `{"items":[
		{"type":"message","role":"developer","content":"first"},
		{"type":"message","role":"user","content":"second"},
		{"type":"message","role":"assistant","content":"third"}
	]}`)

	listReq := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+created.ID+"/items?order=asc&limit=2", nil)
	listRec := httptest.NewRecorder()
	srv.ServeHTTP(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code, listRec.Body.String())

	firstPage := echotest.Decode[core.ConversationItemListResponse](t, listRec)
	require.Equal(t, "list", firstPage.Object)
	require.Len(t, firstPage.Data, 2)
	require.True(t, firstPage.HasMore, "first page = %+v, want two items and has_more", firstPage)
	require.NotNil(t, firstPage.FirstID)
	require.NotNil(t, firstPage.LastID)

	var firstItem map[string]any
	err := json.Unmarshal(firstPage.Data[0], &firstItem)
	require.NoError(t, err)
	require.Equal(t, "developer", firstItem["role"])
	require.Equal(t, "completed", firstItem["status"], "first item = %#v, want normalized developer message", firstItem)

	secondReq := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+created.ID+"/items?order=asc&limit=2&after="+*firstPage.LastID, nil)
	secondRec := httptest.NewRecorder()
	srv.ServeHTTP(secondRec, secondReq)
	secondPage := echotest.Decode[core.ConversationItemListResponse](t, secondRec)
	require.Equal(t, http.StatusOK, secondRec.Code)
	require.Len(t, secondPage.Data, 1)
	require.False(t, secondPage.HasMore, "second page status/data = %d/%+v, want final item", secondRec.Code, secondPage)

	createReq := httptest.NewRequest(http.MethodPost, "/v1/conversations/"+created.ID+"/items",
		strings.NewReader(`{"items":[{"role":"user","content":"fourth"},{"type":"reasoning","summary":[]}]}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, createReq)
	require.Equal(t, http.StatusOK, createRec.Code, createRec.Body.String())

	added := echotest.Decode[core.ConversationItemListResponse](t, createRec)
	require.Len(t, added.Data, 2)
	require.False(t, added.HasMore)
	require.NotNil(t, added.FirstID)
	require.NotNil(t, added.LastID, "created items = %+v, want two-item list", added)

	getReq := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+created.ID+"/items/"+*added.LastID, nil)
	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, getReq)
	require.Equal(t, http.StatusOK, getRec.Code)
	require.Contains(t, getRec.Body.String(), `"type":"reasoning"`)

	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/conversations/"+created.ID+"/items/"+*added.LastID, nil)
	deleteRec := httptest.NewRecorder()
	srv.ServeHTTP(deleteRec, deleteReq)
	require.Equal(t, http.StatusOK, deleteRec.Code, deleteRec.Body.String())

	getAfterDelete := httptest.NewRecorder()
	srv.ServeHTTP(getAfterDelete, getReq)
	require.Equal(t, http.StatusNotFound, getAfterDelete.Code)
}

func TestConversationItemsConcurrentDuplicateIDHasSingleWinner(t *testing.T) {
	srv := New(&mockProvider{}, nil)
	created := createConversation(t, srv, `{}`)

	const writers = 24
	start := make(chan struct{})
	statuses := make(chan int, writers)
	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			<-start
			req := httptest.NewRequest(http.MethodPost, "/v1/conversations/"+created.ID+"/items",
				strings.NewReader(`{"items":[{"id":"msg_shared","type":"message","role":"user","content":"same"}]}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			statuses <- rec.Code
		})
	}
	close(start)
	wg.Wait()
	close(statuses)

	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	require.Equal(t, 1, counts[http.StatusOK])
	require.Equal(t, writers-1, counts[http.StatusBadRequest], "statuses = %v, want one 200 and %d 400s", counts, writers-1)
}

func TestConversationCreateRejectsNonObjectItems(t *testing.T) {
	srv := New(&mockProvider{}, nil)
	for _, body := range []string{`{"items":[42]}`, `{"items":["hello"]}`, `{"items":[null]}`} {
		req := httptest.NewRequest(http.MethodPost, "/v1/conversations", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code, "body %s status = %d (%s), want 400", body, rec.Code, rec.Body.String())

		envelope := echotest.Decode[core.OpenAIErrorEnvelope](t, rec)
		require.NotNil(t, envelope.Error.Param)
		require.Equal(t, "items[0]", *envelope.Error.Param, "body %s param = %v, want items[0]", body, envelope.Error.Param)
	}
}

func TestConversationCreateRejectsNullRequiredFunctionFields(t *testing.T) {
	tests := []struct {
		name string
		item string
	}{
		{
			name: "function call arguments",
			item: `{"type":"function_call","call_id":"call_1","name":"lookup","arguments":null}`,
		},
		{
			name: "function call output",
			item: `{"type":"function_call_output","call_id":"call_1","output":null}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := New(&mockProvider{}, nil)
			req := httptest.NewRequest(http.MethodPost, "/v1/conversations",
				strings.NewReader(`{"items":[`+tt.item+`]}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

			envelope := echotest.Decode[core.OpenAIErrorEnvelope](t, rec)
			require.NotNil(t, envelope.Error.Param)
			require.Equal(t, "items[0]", *envelope.Error.Param)
		})
	}
}

func TestPaginateConversationItemsCapsDirectLimit(t *testing.T) {
	items := make([]json.RawMessage, maxCursorListLimit+1)
	for index := range items {
		items[index] = json.RawMessage(fmt.Sprintf(
			`{"id":"msg_%03d","type":"message","role":"user","content":"item %d"}`,
			index, index,
		))
	}

	page, err := paginateConversationItems(items, core.ConversationItemListParams{
		Limit: maxCursorListLimit + 1,
		Order: "asc",
	})
	require.Nil(t, err)
	require.Len(t, page.Data, maxCursorListLimit)
	require.True(t, page.HasMore)
}

func TestConversationItemListEmptyUsesNullCursors(t *testing.T) {
	srv := New(&mockProvider{}, nil)
	created := createConversation(t, srv, `{}`)

	req := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+created.ID+"/items", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body := echotest.Decode[map[string]any](t, rec)
	first, exists := body["first_id"]
	require.True(t, exists)
	require.Nil(t, first)
	last, exists := body["last_id"]
	require.True(t, exists)
	require.Nil(t, last)
}

func TestConversationItemListUnknownCursorReturnsOpenAICompatible404(t *testing.T) {
	srv := New(&mockProvider{}, nil)
	created := createConversation(t, srv, `{"items":[{"role":"user","content":"hello"}]}`)

	req := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+created.ID+"/items?after=msg_missing", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

	envelope := echotest.Decode[core.OpenAIErrorEnvelope](t, rec)
	require.Equal(t, core.ErrorTypeInvalidRequest, envelope.Error.Type)
	require.NotNil(t, envelope.Error.Param)
	require.Equal(t, "after", *envelope.Error.Param, "error = %+v, want invalid_request_error for after", envelope.Error)
}

func TestConversationItemIncludeControlsOptionalFields(t *testing.T) {
	tests := []struct {
		name    string
		item    string
		include string
		field   string
	}{
		{name: "reasoning encrypted content", item: `{"type":"reasoning","summary":[],"encrypted_content":"secret"}`, include: "reasoning.encrypted_content", field: "encrypted_content"},
		{name: "output text logprobs", item: `{"type":"message","content":[{"type":"output_text","text":"ok","logprobs":[{"token":"ok"}]}]}`, include: "message.output_text.logprobs", field: "logprobs"},
		{name: "input image URL", item: `{"type":"message","content":[{"type":"input_image","image_url":"data:image/png;base64,x"}]}`, include: "message.input_image.image_url", field: "image_url"},
		{name: "file search results", item: `{"type":"file_search_call","results":[{"file_id":"file_1"}]}`, include: "file_search_call.results", field: "results"},
		{name: "web search sources", item: `{"type":"web_search_call","action":{"sources":[{"url":"https://example.com"}]}}`, include: "web_search_call.action.sources", field: "sources"},
		{name: "code interpreter outputs", item: `{"type":"code_interpreter_call","outputs":[{"type":"logs","logs":"ok"}]}`, include: "code_interpreter_call.outputs", field: "outputs"},
		{name: "computer output image", item: `{"type":"computer_call_output","output":{"type":"computer_screenshot","image_url":"data:image/png;base64,x"}}`, include: "computer_call_output.output.image_url", field: "image_url"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			without := string(conversationItemForInclude(json.RawMessage(tt.item), nil))
			require.NotContains(t, without, `"`+tt.field+`"`, "without include = %s, want %s omitted", without, tt.field)

			with := string(conversationItemForInclude(json.RawMessage(tt.item), []string{tt.include}))
			require.Contains(t, with, `"`+tt.field+`"`)
		})
	}
}

func TestConversationItemsPreserveLargeUnknownIntegers(t *testing.T) {
	srv := New(&mockProvider{}, nil)
	created := createConversation(t, srv, `{"items":[{"type":"future_item","opaque_integer":9007199254740993}]}`)

	req := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+created.ID+"/items?order=asc", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"opaque_integer":9007199254740993`)
}

func TestConversationItemProjectionPreservesUnknownNumericFields(t *testing.T) {
	raw := json.RawMessage(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"ok","logprobs":[],"opaque_integer":9007199254740993}]}`)
	projected := conversationItemForInclude(raw, nil)
	require.Contains(t, string(projected), `"opaque_integer":9007199254740993`, "projected item = %s, want exact large integer", projected)
	require.NotContains(t, string(projected), `"logprobs"`, "projected item = %s, want logprobs omitted", projected)
}

func TestConversationDeleteRemovesConversation(t *testing.T) {
	srv := New(&mockProvider{}, nil)
	created := createConversation(t, srv, `{}`)

	delReq := httptest.NewRequest(http.MethodDelete, "/v1/conversations/"+created.ID, nil)
	delRec := httptest.NewRecorder()
	srv.ServeHTTP(delRec, delReq)
	require.Equal(t, http.StatusOK, delRec.Code, delRec.Body.String())

	deleted := echotest.Decode[core.ConversationDeleteResponse](t, delRec)
	require.Equal(t, created.ID, deleted.ID)
	require.Equal(t, "conversation.deleted", deleted.Object)
	require.True(t, deleted.Deleted, "delete response = %+v, want deleted %s", deleted, created.ID)

	getReq := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+created.ID, nil)
	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, getReq)
	require.Equal(t, http.StatusNotFound, getRec.Code, getRec.Body.String())
}

// TestConversationEndpointErrors covers the validation and not-found error
// paths. Each case is independent of stored state: update metadata validation
// runs before the conversation is loaded, so a missing id still exercises it.
func TestConversationEndpointErrors(t *testing.T) {
	bigItems := make([]string, core.MaxConversationInitialItems+1)
	for i := range bigItems {
		bigItems[i] = `{"type":"message","role":"user","content":"x"}`
	}
	bigMetadata := make([]string, 17)
	for i := range bigMetadata {
		bigMetadata[i] = fmt.Sprintf(`"key%d":"value"`, i)
	}

	tests := []struct {
		name           string
		method         string
		path           string
		body           string
		wantStatus     int
		wantErrorType  core.ErrorType
		wantErrorParam string
	}{
		{
			name:          "get missing conversation",
			method:        http.MethodGet,
			path:          "/v1/conversations/conv_missing",
			wantStatus:    http.StatusNotFound,
			wantErrorType: core.ErrorTypeNotFound,
		},
		{
			name:          "update missing conversation",
			method:        http.MethodPost,
			path:          "/v1/conversations/conv_missing",
			body:          `{"metadata":{}}`,
			wantStatus:    http.StatusNotFound,
			wantErrorType: core.ErrorTypeNotFound,
		},
		{
			name:          "delete missing conversation",
			method:        http.MethodDelete,
			path:          "/v1/conversations/conv_missing",
			wantStatus:    http.StatusNotFound,
			wantErrorType: core.ErrorTypeNotFound,
		},
		{
			name:           "update without metadata",
			method:         http.MethodPost,
			path:           "/v1/conversations/conv_missing",
			body:           `{}`,
			wantStatus:     http.StatusBadRequest,
			wantErrorType:  core.ErrorTypeInvalidRequest,
			wantErrorParam: "metadata",
		},
		{
			name:           "create with too many items",
			method:         http.MethodPost,
			path:           "/v1/conversations",
			body:           fmt.Sprintf(`{"items":[%s]}`, strings.Join(bigItems, ",")),
			wantStatus:     http.StatusBadRequest,
			wantErrorType:  core.ErrorTypeInvalidRequest,
			wantErrorParam: "items",
		},
		{
			name:           "create with too much metadata",
			method:         http.MethodPost,
			path:           "/v1/conversations",
			body:           fmt.Sprintf(`{"metadata":{%s}}`, strings.Join(bigMetadata, ",")),
			wantStatus:     http.StatusBadRequest,
			wantErrorType:  core.ErrorTypeInvalidRequest,
			wantErrorParam: "metadata",
		},
		{
			name:          "create with invalid json",
			method:        http.MethodPost,
			path:          "/v1/conversations",
			body:          `{`,
			wantStatus:    http.StatusBadRequest,
			wantErrorType: core.ErrorTypeInvalidRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := New(&mockProvider{}, nil)

			var body io.Reader
			if tt.body != "" {
				body = strings.NewReader(tt.body)
			}
			req := httptest.NewRequest(tt.method, tt.path, body)
			if tt.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			envelope := echotest.Decode[core.OpenAIErrorEnvelope](t, rec)

			if tt.wantErrorType != "" {
				require.Equal(t, tt.wantErrorType, envelope.Error.Type)
			}
			if tt.wantErrorParam != "" {
				require.NotNil(t, envelope.Error.Param)
				require.Equal(t, tt.wantErrorParam, *envelope.Error.Param)
			}
		})
	}
}

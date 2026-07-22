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

	"github.com/enterpilot/gomodel/internal/core"
)

func createConversation(t *testing.T, srv *Server, body string) core.Conversation {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/conversations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var conversation core.Conversation
	if err := json.Unmarshal(rec.Body.Bytes(), &conversation); err != nil {
		t.Fatalf("decode conversation: %v", err)
	}
	return conversation
}

func TestConversationCreateReturnsOpenAICompatibleObject(t *testing.T) {
	srv := New(&mockProvider{}, nil)

	conversation := createConversation(t, srv, `{"metadata":{"topic":"demo"}}`)

	if !strings.HasPrefix(conversation.ID, "conv_") {
		t.Fatalf("id = %q, want conv_ prefix", conversation.ID)
	}
	if conversation.Object != "conversation" {
		t.Fatalf("object = %q, want conversation", conversation.Object)
	}
	if conversation.CreatedAt <= 0 {
		t.Fatalf("created_at = %d, want positive", conversation.CreatedAt)
	}
	if conversation.Metadata["topic"] != "demo" {
		t.Fatalf("metadata[topic] = %q, want demo", conversation.Metadata["topic"])
	}
}

func TestConversationCreateEmptyBodyYieldsEmptyMetadataObject(t *testing.T) {
	srv := New(&mockProvider{}, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/conversations", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var conversation core.Conversation
	if err := json.Unmarshal(rec.Body.Bytes(), &conversation); err != nil {
		t.Fatalf("decode conversation: %v", err)
	}
	// metadata must be an empty object rather than null, matching OpenAI.
	if conversation.Metadata == nil || len(conversation.Metadata) != 0 {
		t.Fatalf("metadata = %#v, want empty object", conversation.Metadata)
	}
}

func TestConversationCreateAcceptsItems(t *testing.T) {
	srv := New(&mockProvider{}, nil)

	conversation := createConversation(t, srv,
		`{"items":[{"type":"message","role":"user","content":"hello"}]}`)
	if conversation.ID == "" {
		t.Fatal("conversation id is empty")
	}
}

func TestConversationGetRoundTrip(t *testing.T) {
	srv := New(&mockProvider{}, nil)
	created := createConversation(t, srv, `{"metadata":{"k":"v"}}`)

	req := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+created.ID, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var got core.Conversation
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode conversation: %v", err)
	}
	if got.ID != created.ID || got.Metadata["k"] != "v" {
		t.Fatalf("get conversation = %+v, want id %s metadata k=v", got, created.ID)
	}
}

func TestConversationUpdateMergesMetadata(t *testing.T) {
	srv := New(&mockProvider{}, nil)
	created := createConversation(t, srv, `{"metadata":{"old":"value","keep":"gone"}}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/conversations/"+created.ID,
		strings.NewReader(`{"metadata":{"new":"value"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var updated core.Conversation
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode conversation: %v", err)
	}
	if updated.Metadata["new"] != "value" {
		t.Fatalf("metadata[new] = %q, want value", updated.Metadata["new"])
	}
	if updated.Metadata["old"] != "value" || updated.Metadata["keep"] != "gone" {
		t.Fatalf("metadata = %v, want existing keys preserved", updated.Metadata)
	}
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
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("update status = %d (%s), want 400", rec.Code, rec.Body.String())
	}
	var envelope core.OpenAIErrorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if envelope.Error.Param == nil || *envelope.Error.Param != "metadata" || envelope.Error.Code == nil || *envelope.Error.Code != "metadata_max_properties_exceeded" {
		t.Fatalf("error = %+v, want metadata_max_properties_exceeded", envelope.Error)
	}
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
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d (%s), want 200", listRec.Code, listRec.Body.String())
	}
	var firstPage core.ConversationItemListResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &firstPage); err != nil {
		t.Fatalf("decode first page: %v", err)
	}
	if firstPage.Object != "list" || len(firstPage.Data) != 2 || !firstPage.HasMore {
		t.Fatalf("first page = %+v, want two items and has_more", firstPage)
	}
	if firstPage.FirstID == nil || firstPage.LastID == nil {
		t.Fatalf("first/last id = %v/%v, want stable ids", firstPage.FirstID, firstPage.LastID)
	}
	var firstItem map[string]any
	if err := json.Unmarshal(firstPage.Data[0], &firstItem); err != nil {
		t.Fatalf("decode first item: %v", err)
	}
	if firstItem["role"] != "developer" || firstItem["status"] != "completed" {
		t.Fatalf("first item = %#v, want normalized developer message", firstItem)
	}

	secondReq := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+created.ID+"/items?order=asc&limit=2&after="+*firstPage.LastID, nil)
	secondRec := httptest.NewRecorder()
	srv.ServeHTTP(secondRec, secondReq)
	var secondPage core.ConversationItemListResponse
	if err := json.Unmarshal(secondRec.Body.Bytes(), &secondPage); err != nil {
		t.Fatalf("decode second page: %v", err)
	}
	if secondRec.Code != http.StatusOK || len(secondPage.Data) != 1 || secondPage.HasMore {
		t.Fatalf("second page status/data = %d/%+v, want final item", secondRec.Code, secondPage)
	}

	createReq := httptest.NewRequest(http.MethodPost, "/v1/conversations/"+created.ID+"/items",
		strings.NewReader(`{"items":[{"role":"user","content":"fourth"},{"type":"reasoning","summary":[]}]}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create items status = %d (%s), want 200", createRec.Code, createRec.Body.String())
	}
	var added core.ConversationItemListResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &added); err != nil {
		t.Fatalf("decode created items: %v", err)
	}
	if len(added.Data) != 2 || added.HasMore || added.FirstID == nil || added.LastID == nil {
		t.Fatalf("created items = %+v, want two-item list", added)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+created.ID+"/items/"+*added.LastID, nil)
	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK || !strings.Contains(getRec.Body.String(), `"type":"reasoning"`) {
		t.Fatalf("get item status/body = %d/%s", getRec.Code, getRec.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/conversations/"+created.ID+"/items/"+*added.LastID, nil)
	deleteRec := httptest.NewRecorder()
	srv.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete item status = %d (%s), want 200", deleteRec.Code, deleteRec.Body.String())
	}
	getAfterDelete := httptest.NewRecorder()
	srv.ServeHTTP(getAfterDelete, getReq)
	if getAfterDelete.Code != http.StatusNotFound {
		t.Fatalf("get deleted item status = %d, want 404", getAfterDelete.Code)
	}
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
	if counts[http.StatusOK] != 1 || counts[http.StatusBadRequest] != writers-1 {
		t.Fatalf("statuses = %v, want one 200 and %d 400s", counts, writers-1)
	}
}

func TestConversationCreateRejectsNonObjectItems(t *testing.T) {
	srv := New(&mockProvider{}, nil)
	for _, body := range []string{`{"items":[42]}`, `{"items":["hello"]}`, `{"items":[null]}`} {
		req := httptest.NewRequest(http.MethodPost, "/v1/conversations", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s status = %d (%s), want 400", body, rec.Code, rec.Body.String())
		}
		var envelope core.OpenAIErrorEnvelope
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode error: %v", err)
		}
		if envelope.Error.Param == nil || *envelope.Error.Param != "items[0]" {
			t.Fatalf("body %s param = %v, want items[0]", body, envelope.Error.Param)
		}
	}
}

func TestConversationItemListEmptyUsesNullCursors(t *testing.T) {
	srv := New(&mockProvider{}, nil)
	created := createConversation(t, srv, `{}`)

	req := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+created.ID+"/items", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if first, exists := body["first_id"]; !exists || first != nil {
		t.Fatalf("first_id = %#v (exists %v), want null", first, exists)
	}
	if last, exists := body["last_id"]; !exists || last != nil {
		t.Fatalf("last_id = %#v (exists %v), want null", last, exists)
	}
}

func TestConversationItemListUnknownCursorReturnsOpenAICompatible404(t *testing.T) {
	srv := New(&mockProvider{}, nil)
	created := createConversation(t, srv, `{"items":[{"role":"user","content":"hello"}]}`)

	req := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+created.ID+"/items?after=msg_missing", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("list status = %d (%s), want 404", rec.Code, rec.Body.String())
	}
	var envelope core.OpenAIErrorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if envelope.Error.Type != core.ErrorTypeInvalidRequest || envelope.Error.Param == nil || *envelope.Error.Param != "after" {
		t.Fatalf("error = %+v, want invalid_request_error for after", envelope.Error)
	}
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
			if strings.Contains(without, `"`+tt.field+`"`) {
				t.Fatalf("without include = %s, want %s omitted", without, tt.field)
			}
			with := string(conversationItemForInclude(json.RawMessage(tt.item), []string{tt.include}))
			if !strings.Contains(with, `"`+tt.field+`"`) {
				t.Fatalf("with include = %s, want %s retained", with, tt.field)
			}
		})
	}
}

func TestConversationItemsPreserveLargeUnknownIntegers(t *testing.T) {
	srv := New(&mockProvider{}, nil)
	created := createConversation(t, srv, `{"items":[{"type":"future_item","opaque_integer":9007199254740993}]}`)

	req := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+created.ID+"/items?order=asc", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"opaque_integer":9007199254740993`) {
		t.Fatalf("list body = %s, want exact large integer", rec.Body.String())
	}
}

func TestConversationItemProjectionPreservesUnknownNumericFields(t *testing.T) {
	raw := json.RawMessage(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"ok","logprobs":[],"opaque_integer":9007199254740993}]}`)
	projected := conversationItemForInclude(raw, nil)
	if !strings.Contains(string(projected), `"opaque_integer":9007199254740993`) {
		t.Fatalf("projected item = %s, want exact large integer", projected)
	}
	if strings.Contains(string(projected), `"logprobs"`) {
		t.Fatalf("projected item = %s, want logprobs omitted", projected)
	}
}

func TestConversationDeleteRemovesConversation(t *testing.T) {
	srv := New(&mockProvider{}, nil)
	created := createConversation(t, srv, `{}`)

	delReq := httptest.NewRequest(http.MethodDelete, "/v1/conversations/"+created.ID, nil)
	delRec := httptest.NewRecorder()
	srv.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200 (%s)", delRec.Code, delRec.Body.String())
	}
	var deleted core.ConversationDeleteResponse
	if err := json.Unmarshal(delRec.Body.Bytes(), &deleted); err != nil {
		t.Fatalf("decode delete response: %v", err)
	}
	if deleted.ID != created.ID || deleted.Object != "conversation.deleted" || !deleted.Deleted {
		t.Fatalf("delete response = %+v, want deleted %s", deleted, created.ID)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+created.ID, nil)
	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusNotFound {
		t.Fatalf("get after delete status = %d, want 404 (%s)", getRec.Code, getRec.Body.String())
	}
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

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.wantStatus, rec.Body.String())
			}

			var envelope core.OpenAIErrorEnvelope
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode error envelope: %v", err)
			}
			if tt.wantErrorType != "" && envelope.Error.Type != tt.wantErrorType {
				t.Fatalf("error type = %q, want %q", envelope.Error.Type, tt.wantErrorType)
			}
			if tt.wantErrorParam != "" {
				if envelope.Error.Param == nil || *envelope.Error.Param != tt.wantErrorParam {
					t.Fatalf("error param = %v, want %q", envelope.Error.Param, tt.wantErrorParam)
				}
			}
		})
	}
}

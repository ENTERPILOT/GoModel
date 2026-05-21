package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gomodel/internal/core"
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
	// metadata must serialize as {} rather than null to match OpenAI.
	if !strings.Contains(rec.Body.String(), `"metadata":{}`) {
		t.Fatalf("body = %s, want metadata {}", rec.Body.String())
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

func TestConversationGetMissingReturnsNotFound(t *testing.T) {
	srv := New(&mockProvider{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/conversations/conv_missing", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get status = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
	var envelope core.OpenAIErrorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if envelope.Error.Type != core.ErrorTypeNotFound {
		t.Fatalf("error type = %q, want not_found_error", envelope.Error.Type)
	}
}

func TestConversationUpdateReplacesMetadata(t *testing.T) {
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
	if _, ok := updated.Metadata["old"]; ok {
		t.Fatal("metadata still carries replaced key 'old'")
	}
}

func TestConversationUpdateRequiresMetadata(t *testing.T) {
	srv := New(&mockProvider{}, nil)
	created := createConversation(t, srv, `{}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/conversations/"+created.ID,
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("update status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	var envelope core.OpenAIErrorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if envelope.Error.Param == nil || *envelope.Error.Param != "metadata" {
		t.Fatalf("error param = %v, want metadata", envelope.Error.Param)
	}
}

func TestConversationUpdateMissingReturnsNotFound(t *testing.T) {
	srv := New(&mockProvider{}, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/conversations/conv_missing",
		strings.NewReader(`{"metadata":{}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("update status = %d, want 404 (%s)", rec.Code, rec.Body.String())
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

func TestConversationDeleteMissingReturnsNotFound(t *testing.T) {
	srv := New(&mockProvider{}, nil)

	req := httptest.NewRequest(http.MethodDelete, "/v1/conversations/conv_missing", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete status = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

func TestConversationCreateRejectsTooManyItems(t *testing.T) {
	srv := New(&mockProvider{}, nil)

	items := make([]string, core.MaxConversationInitialItems+1)
	for i := range items {
		items[i] = `{"type":"message","role":"user","content":"x"}`
	}
	body := fmt.Sprintf(`{"items":[%s]}`, strings.Join(items, ","))

	req := httptest.NewRequest(http.MethodPost, "/v1/conversations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	var envelope core.OpenAIErrorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if envelope.Error.Param == nil || *envelope.Error.Param != "items" {
		t.Fatalf("error param = %v, want items", envelope.Error.Param)
	}
}

func TestConversationCreateRejectsTooMuchMetadata(t *testing.T) {
	srv := New(&mockProvider{}, nil)

	pairs := make([]string, 17)
	for i := range pairs {
		pairs[i] = fmt.Sprintf(`"key%d":"value"`, i)
	}
	body := fmt.Sprintf(`{"metadata":{%s}}`, strings.Join(pairs, ","))

	req := httptest.NewRequest(http.MethodPost, "/v1/conversations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestConversationCreateRejectsInvalidJSON(t *testing.T) {
	srv := New(&mockProvider{}, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/conversations", strings.NewReader(`{`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

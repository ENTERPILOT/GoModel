package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

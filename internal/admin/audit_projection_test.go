package admin

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/auditlog"
)

// fullAuditEntry builds an entry carrying every heavy payload the list
// projection is expected to strip.
func fullAuditEntry(id string) auditlog.LogEntry {
	return auditlog.LogEntry{
		ID:             id,
		Timestamp:      time.Now().UTC(),
		RequestedModel: "gpt-4o",
		Provider:       "openai",
		StatusCode:     200,
		RequestID:      "req-" + id,
		Method:         http.MethodPost,
		Path:           "/v1/messages",
		Data: &auditlog.LogData{
			ErrorMessage:    "boom",
			RequestHeaders:  map[string]string{"content-type": "application/json"},
			ResponseHeaders: map[string]string{"content-type": "application/json"},
			RequestBody: map[string]any{
				"model":    "gpt-4o",
				"messages": []any{map[string]any{"role": "user", "content": "hello"}},
			},
			ResponseBody: map[string]any{
				"choices": []any{map[string]any{"message": map[string]any{"content": "hi"}}},
			},
			Attempts: []auditlog.AttemptSnapshot{
				{
					Seq:             1,
					Kind:            "provider",
					ProviderName:    "primary-openai",
					StatusCode:      500,
					ErrorMessage:    "upstream error",
					ResponseBody:    map[string]any{"error": "rate limited"},
					ResponseHeaders: map[string]string{"retry-after": "1"},
				},
				{Seq: 2, Kind: "provider", ProviderName: "primary-openai", StatusCode: 200, Success: true},
			},
			RequestRevisions: []auditlog.RequestRevisionSnapshot{
				{
					Seq:         1,
					Rewriter:    "pro-token-compression",
					BytesBefore: 2000,
					BytesAfter:  1000,
					TokensSaved: 250,
					Body:        map[string]any{"model": "gpt-4o", "messages": []any{}},
					Detail:      map[string]any{"blocks_replaced": float64(3)},
				},
			},
		},
	}
}

func TestAuditLogSlimsListEntries(t *testing.T) {
	reader := &mockAuditReader{
		logResult: &auditlog.LogListResult{
			Entries: []auditlog.LogEntry{fullAuditEntry("log-1")},
			Total:   1, Limit: 25,
		},
	}
	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := newHandlerContext("/admin/audit/log?days=7")

	if err := h.AuditLog(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result struct {
		Entries []struct {
			auditlog.LogEntry
			BodiesOmitted       bool `json:"bodies_omitted"`
			ConversationPayload bool `json:"conversation_payload"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result.Entries))
	}
	entry := result.Entries[0]
	d := entry.Data
	if d == nil {
		t.Fatal("entry data must survive slimming")
	}
	if d.RequestBody != nil || d.ResponseBody != nil {
		t.Error("list entry bodies must be stripped")
	}
	if len(d.Attempts) != 2 {
		t.Fatalf("attempt metadata must survive, got %d attempts", len(d.Attempts))
	}
	if d.Attempts[0].ResponseBody != nil || d.Attempts[0].ResponseHeaders != nil {
		t.Error("attempt error payloads must be stripped")
	}
	if d.Attempts[0].ErrorMessage != "upstream error" || d.Attempts[0].StatusCode != 500 {
		t.Error("attempt scalar fields must survive (they drive the pips)")
	}
	if len(d.RequestRevisions) != 1 {
		t.Fatalf("revision metadata must survive, got %d revisions", len(d.RequestRevisions))
	}
	rev := d.RequestRevisions[0]
	if rev.Body != nil {
		t.Error("revision body must be stripped")
	}
	if rev.TokensSaved != 250 || rev.BytesBefore != 2000 || rev.Detail == nil {
		t.Errorf("revision metadata must survive, got %+v", rev)
	}
	if d.ErrorMessage != "boom" || d.RequestHeaders == nil || d.ResponseHeaders == nil {
		t.Error("small fields must survive slimming")
	}
	if !entry.BodiesOmitted {
		t.Error("bodies_omitted must mark the slim entry")
	}
	// Path is /v1/messages: the dashboard cannot sniff drawer eligibility
	// from the (removed) bodies, so the server-computed flag must carry it.
	if !entry.ConversationPayload {
		t.Error("conversation_payload must be set for a conversation-shaped body")
	}
}

// A body-less entry (LOGGING_LOG_BODIES=false) has nothing to strip; it must
// not claim a detail fetch is worthwhile.
func TestAuditLogLeavesBodylessEntriesUnmarked(t *testing.T) {
	entry := fullAuditEntry("log-1")
	entry.Data = &auditlog.LogData{ErrorMessage: "boom"}
	reader := &mockAuditReader{
		logResult: &auditlog.LogListResult{Entries: []auditlog.LogEntry{entry}, Total: 1, Limit: 25},
	}
	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := newHandlerContext("/admin/audit/log?days=7")

	if err := h.AuditLog(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result struct {
		Entries []struct {
			BodiesOmitted bool `json:"bodies_omitted"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if result.Entries[0].BodiesOmitted {
		t.Error("an entry with no captured payload must not be marked bodies_omitted")
	}
}

// The detail endpoint is the designated source of full payloads and must not
// be slimmed.
func TestAuditLogDetailKeepsFullPayload(t *testing.T) {
	entry := fullAuditEntry("log-1")
	reader := &mockAuditReader{logByID: &entry}
	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := newHandlerContext("/admin/audit/detail?log_id=log-1")

	if err := h.AuditLogDetail(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var got struct {
		auditlog.LogEntry
		BodiesOmitted bool `json:"bodies_omitted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	d := got.Data
	if d == nil || d.RequestBody == nil || d.ResponseBody == nil {
		t.Fatal("detail must keep request/response bodies")
	}
	if d.Attempts[0].ResponseBody == nil {
		t.Error("detail must keep attempt error payloads")
	}
	if d.RequestRevisions[0].Body == nil {
		t.Error("detail must keep revision bodies")
	}
	if got.BodiesOmitted {
		t.Error("detail entries must not be marked bodies_omitted")
	}
}

func TestAuditConversationSlimsEntries(t *testing.T) {
	reader := &mockAuditReader{
		conversationResult: &auditlog.ConversationResult{
			AnchorID: "log-1",
			Entries:  []auditlog.LogEntry{fullAuditEntry("log-1")},
		},
	}
	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := newHandlerContext("/admin/audit/conversation?log_id=log-1")

	if err := h.AuditConversation(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result auditlog.ConversationResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	d := result.Entries[0].Data
	if d == nil {
		t.Fatal("entry data must survive")
	}
	if d.RequestBody == nil || d.ResponseBody == nil {
		t.Error("the transcript is built from the bodies; they must survive")
	}
	if d.ErrorMessage != "boom" {
		t.Error("error_message feeds the drawer's error rendering; it must survive")
	}
	if d.Attempts != nil || d.ResponseHeaders != nil {
		t.Errorf("attempts/response headers must be stripped from conversation entries, got %+v", d)
	}
	if len(d.RequestRevisions) != 1 {
		t.Fatalf("revision metadata feeds the drawer's request-step picker; it must survive, got %+v", d.RequestRevisions)
	}
	rev := d.RequestRevisions[0]
	if rev.Body != nil || rev.Detail != nil {
		t.Errorf("revision bodies/details must be stripped from conversation entries, got %+v", rev)
	}
	if rev.Rewriter != "pro-token-compression" || rev.Seq != 1 || rev.BytesBefore != 2000 || rev.BytesAfter != 1000 {
		t.Errorf("revision metadata must survive, got %+v", rev)
	}
	if d.RequestHeaders["content-type"] != "application/json" {
		t.Errorf("redacted request headers are required for follow-ups, got %+v", d.RequestHeaders)
	}
}

func TestHasConversationPayload(t *testing.T) {
	tests := []struct {
		name     string
		request  any
		response any
		want     bool
	}{
		{"chat messages", map[string]any{"messages": []any{}}, nil, true},
		{"responses input", map[string]any{"input": "hello"}, nil, true},
		{"responses instructions", map[string]any{"instructions": "be brief"}, nil, true},
		{"responses chaining", map[string]any{"previous_response_id": "resp_1"}, nil, true},
		{"chat choices", nil, map[string]any{"choices": []any{}}, true},
		{"responses output", nil, map[string]any{"output": []any{map[string]any{"type": "message"}}}, true},
		{"embeddings", map[string]any{"model": "text-embedding-3-small", "dimensions": float64(256)}, map[string]any{"data": []any{}}, false},
		{"nil bodies", nil, nil, false},
		{"non-object bodies", "raw", "raw", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasConversationPayload(tt.request, tt.response); got != tt.want {
				t.Errorf("hasConversationPayload() = %v, want %v", got, tt.want)
			}
		})
	}
}

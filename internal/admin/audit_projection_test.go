package admin

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			Guardrails: []auditlog.GuardrailOutcomeSnapshot{
				{Seq: 1, Phase: "prompt", Instance: "check", Action: "warn", Code: "pii", Detail: map[string]any{"hits": float64(2)}},
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
	c, rec := echotest.Get(t, "/admin/audit/log?days=7")
	require.NoError(t, h.AuditLog(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var result struct {
		Entries []struct {
			auditlog.LogEntry
			BodiesOmitted       bool `json:"bodies_omitted"`
			ConversationPayload bool `json:"conversation_payload"`
		} `json:"entries"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	require.Len(t, result.Entries, 1)

	entry := result.Entries[0]
	d := entry.Data
	require.NotNil(t, d, "entry data must survive slimming")
	assert.Nil(t, d.RequestBody)
	assert.Nil(t, d.ResponseBody)
	require.Len(t, d.Attempts, 2)
	assert.Nil(t, d.Attempts[0].ResponseBody)
	assert.Nil(t, d.Attempts[0].ResponseHeaders)
	assert.Equal(t, "upstream error", d.Attempts[0].ErrorMessage)
	assert.Equal(t, 500, d.Attempts[0].StatusCode)
	require.Len(t, d.RequestRevisions, 1)

	rev := d.RequestRevisions[0]
	assert.Nil(t, rev.Body)
	assert.Equal(t, 250, rev.TokensSaved)
	assert.Equal(t, 2000, rev.BytesBefore)
	assert.NotNil(t, rev.Detail, "revision metadata must survive")
	require.Len(t, d.Guardrails, 1)
	require.Equal(t, "warn", d.Guardrails[0].Action)
	require.Equal(t, "pii", d.Guardrails[0].Code)
	assert.Nil(t, d.Guardrails[0].Detail)
	assert.Equal(t, "boom", d.ErrorMessage)
	assert.NotNil(t, d.RequestHeaders)
	assert.NotNil(t, d.ResponseHeaders)
	assert.True(t, entry.BodiesOmitted)

	// Path is /v1/messages: the dashboard cannot sniff drawer eligibility
	// from the (removed) bodies, so the server-computed flag must carry it.
	assert.True(t, entry.ConversationPayload)
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
	c, rec := echotest.Get(t, "/admin/audit/log?days=7")
	require.NoError(t, h.AuditLog(c))

	var result struct {
		Entries []struct {
			BodiesOmitted bool `json:"bodies_omitted"`
		} `json:"entries"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.False(t, result.Entries[0].BodiesOmitted)
}

// The detail endpoint is the designated source of full payloads and must not
// be slimmed.
func TestAuditLogDetailKeepsFullPayload(t *testing.T) {
	entry := fullAuditEntry("log-1")
	reader := &mockAuditReader{logByID: &entry}
	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := echotest.Get(t, "/admin/audit/detail?log_id=log-1")
	require.NoError(t, h.AuditLogDetail(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var got struct {
		auditlog.LogEntry
		BodiesOmitted bool `json:"bodies_omitted"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))

	d := got.Data
	require.NotNil(t, d)
	require.NotNil(t, d.RequestBody)
	require.NotNil(t, d.ResponseBody)
	assert.NotNil(t, d.Attempts[0].ResponseBody)
	assert.NotNil(t, d.RequestRevisions[0].Body)
	assert.False(t, got.BodiesOmitted)
}

func TestAuditConversationSlimsEntries(t *testing.T) {
	reader := &mockAuditReader{
		conversationResult: &auditlog.ConversationResult{
			AnchorID: "log-1",
			Entries:  []auditlog.LogEntry{fullAuditEntry("log-1")},
		},
	}
	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := echotest.Get(t, "/admin/audit/conversation?log_id=log-1")
	require.NoError(t, h.AuditConversation(c))
	require.Equal(t, http.StatusOK, rec.Code)

	result := echotest.Decode[auditlog.ConversationResult](t, rec)

	d := result.Entries[0].Data
	require.NotNil(t, d, "entry data must survive slimming")
	assert.NotNil(t, d.RequestBody)
	assert.NotNil(t, d.ResponseBody)
	assert.Equal(t, "boom", d.ErrorMessage)
	assert.Nil(t, d.Attempts)
	assert.Nil(t, d.ResponseHeaders)
	assert.Nil(t, d.Guardrails, "attempts/response headers/guardrail outcomes must be stripped from conversation entries")
	require.Len(t, d.RequestRevisions, 1)

	rev := d.RequestRevisions[0]
	assert.Nil(t, rev.Body)
	assert.Nil(t, rev.Detail, "revision bodies/details must be stripped from conversation entries")
	assert.Equal(t, "pro-token-compression", rev.Rewriter)
	assert.Equal(t, 1, rev.Seq)
	assert.Equal(t, 2000, rev.BytesBefore)
	assert.Equal(t, 1000, rev.BytesAfter, "revision metadata must survive")
	assert.Equal(t, "application/json", d.RequestHeaders["content-type"], "redacted request headers are required for follow-ups")
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
			got := hasConversationPayload(tt.request, tt.response)
			assert.Equal(t, tt.want, got)
		})
	}
}

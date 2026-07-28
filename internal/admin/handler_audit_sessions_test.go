package admin

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/auditlog"
)

func TestAuditSessions_NilReader(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := newHandlerContext("/admin/audit/sessions")

	if err := h.AuditSessions(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result auditlog.SessionListResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(result.Sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(result.Sessions))
	}
	if result.Limit != 25 {
		t.Errorf("expected default echoed limit 25, got %d", result.Limit)
	}
}

func TestAuditSessions_Success(t *testing.T) {
	now := time.Now().UTC()
	reader := &mockAuditReader{
		sessionsResult: &auditlog.SessionListResult{
			Sessions: []auditlog.SessionSummary{
				{
					SessionID:      "sess-a",
					Count:          3,
					FirstTimestamp: now.Add(-time.Minute),
					LastTimestamp:  now,
					Latest: auditlog.LogEntry{
						ID:        "log-3",
						Timestamp: now,
						SessionID: "sess-a",
						Provider:  "openai",
						RequestID: "req-3",
					},
				},
				{
					Count:          1,
					FirstTimestamp: now.Add(-time.Hour),
					LastTimestamp:  now.Add(-time.Hour),
					Latest: auditlog.LogEntry{
						ID:        "log-1",
						Timestamp: now.Add(-time.Hour),
						Provider:  "openai",
						RequestID: "req-1",
					},
				},
			},
			Total:  2,
			Limit:  25,
			Offset: 0,
		},
	}

	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := newHandlerContext("/admin/audit/sessions?days=7&user_path=/team")

	if err := h.AuditSessions(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if reader.lastQuery.UserPath != "/team" {
		t.Errorf("user_path filter not forwarded: %q", reader.lastQuery.UserPath)
	}

	var result struct {
		Sessions []struct {
			SessionID string             `json:"session_id"`
			Count     int                `json:"count"`
			Latest    *auditlog.LogEntry `json:"latest"`
		} `json:"sessions"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if result.Total != 2 || len(result.Sessions) != 2 {
		t.Fatalf("total=%d sessions=%d, want 2/2", result.Total, len(result.Sessions))
	}
	if result.Sessions[0].SessionID != "sess-a" || result.Sessions[0].Count != 3 {
		t.Errorf("first session = %+v", result.Sessions[0])
	}
	if result.Sessions[0].Latest == nil || result.Sessions[0].Latest.ID != "log-3" {
		t.Errorf("latest entry not embedded: %+v", result.Sessions[0].Latest)
	}
	if result.Sessions[1].SessionID != "" || result.Sessions[1].Count != 1 {
		t.Errorf("singleton thread = %+v", result.Sessions[1])
	}
}

func TestAuditSessions_SlimsLatestEntries(t *testing.T) {
	entry := fullAuditEntry("log-1")
	entry.SessionID = "sess-a"
	reader := &mockAuditReader{
		sessionsResult: &auditlog.SessionListResult{
			Sessions: []auditlog.SessionSummary{{
				SessionID:      "sess-a",
				Count:          2,
				FirstTimestamp: entry.Timestamp.Add(-time.Minute),
				LastTimestamp:  entry.Timestamp,
				Latest:         entry,
			}},
			Total: 1,
			Limit: 25,
		},
	}
	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := newHandlerContext("/admin/audit/sessions")

	if err := h.AuditSessions(c); err != nil {
		t.Fatalf("AuditSessions() error = %v", err)
	}
	var result struct {
		Sessions []struct {
			Latest struct {
				auditlog.LogEntry
				BodiesOmitted bool `json:"bodies_omitted"`
			} `json:"latest"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	latest := result.Sessions[0].Latest
	if latest.SessionID != "sess-a" {
		t.Fatalf("session id = %q, want sess-a", latest.SessionID)
	}
	if latest.Data == nil || latest.Data.RequestBody != nil || latest.Data.ResponseBody != nil {
		t.Fatal("session head must use the slim list payload")
	}
	if !latest.BodiesOmitted {
		t.Fatal("session head with stripped bodies must set bodies_omitted")
	}
}

func TestAuditLog_SessionIDFilterForwarded(t *testing.T) {
	reader := &mockAuditReader{logResult: &auditlog.LogListResult{}}
	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, _ := newHandlerContext("/admin/audit/log?session_id=sess-a")

	if err := h.AuditLog(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reader.lastQuery.SessionID != "sess-a" {
		t.Errorf("session_id filter not forwarded: %q", reader.lastQuery.SessionID)
	}
}

// A session_id filter without explicit date parameters queries the whole
// session; explicit dates still apply so the two can be combined.
func TestAuditLog_SessionIDSkipsDefaultDateWindow(t *testing.T) {
	reader := &mockAuditReader{logResult: &auditlog.LogListResult{}}
	h := NewHandler(nil, nil, WithAuditReader(reader))

	c, _ := newHandlerContext("/admin/audit/log?session_id=sess-a")
	if err := h.AuditLog(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reader.lastQuery.StartDate.IsZero() || !reader.lastQuery.EndDate.IsZero() {
		t.Fatalf("session-only query must be unbounded, got %v..%v",
			reader.lastQuery.StartDate, reader.lastQuery.EndDate)
	}

	c, _ = newHandlerContext("/admin/audit/log?session_id=sess-a&days=7")
	if err := h.AuditLog(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reader.lastQuery.StartDate.IsZero() || reader.lastQuery.EndDate.IsZero() {
		t.Fatal("explicit days must still bound a session query")
	}
}

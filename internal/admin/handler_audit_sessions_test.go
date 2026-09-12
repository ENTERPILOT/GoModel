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

func TestAuditSessions_NilReader(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := echotest.Get(t, "/admin/audit/sessions")
	require.NoError(t, h.AuditSessions(c))
	require.Equal(t, http.StatusOK, rec.Code)

	result := echotest.Decode[auditlog.SessionListResult](t, rec)
	assert.Empty(t, result.Sessions)
	assert.Equal(t, 25, result.Limit)
}

func TestAuditSessions_Success(t *testing.T) {
	now := time.Now().UTC()
	reader := &mockAuditReader{
		sessionsResult: &auditlog.SessionListResult{
			Sessions: []auditlog.SessionSummary{
				{
					SessionID:    "sess-a",
					RequestCount: 6,
					Latest: auditlog.LogEntry{
						ID:        "log-3",
						Timestamp: now,
						SessionID: "sess-a",
						Provider:  "openai",
						RequestID: "req-3",
					},
				},
				{
					RequestCount: 1,
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
	c, rec := echotest.Get(t, "/admin/audit/sessions?days=7&user_path=/team")
	require.NoError(t, h.AuditSessions(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "/team", reader.lastQuery.UserPath)

	var result struct {
		Sessions []struct {
			RequestCount int                `json:"request_count"`
			Latest       *auditlog.LogEntry `json:"latest"`
		} `json:"sessions"`
		Total int `json:"total"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	require.Equal(t, 2, result.Total)
	require.Len(t, result.Sessions, 2)
	assert.Equal(t, 6, result.Sessions[0].RequestCount)
	assert.NotNil(t, result.Sessions[0].Latest)
	assert.Equal(t, "log-3", result.Sessions[0].Latest.ID)
	assert.Equal(t, 1, result.Sessions[1].RequestCount, "singleton thread")
}

func TestAuditSessions_SlimsLatestEntries(t *testing.T) {
	entry := fullAuditEntry("log-1")
	entry.SessionID = "sess-a"
	reader := &mockAuditReader{
		sessionsResult: &auditlog.SessionListResult{
			Sessions: []auditlog.SessionSummary{{
				SessionID:    "sess-a",
				RequestCount: 2,
				Latest:       entry,
			}},
			Total: 1,
			Limit: 25,
		},
	}
	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, rec := echotest.Get(t, "/admin/audit/sessions")
	require.NoError(t, h.AuditSessions(c))

	var result struct {
		Sessions []struct {
			Latest struct {
				auditlog.LogEntry
				BodiesOmitted bool `json:"bodies_omitted"`
			} `json:"latest"`
		} `json:"sessions"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))

	latest := result.Sessions[0].Latest
	require.Equal(t, "sess-a", latest.SessionID)
	require.NotNil(t, latest.Data)
	require.Nil(t, latest.Data.RequestBody)
	require.Nil(t, latest.Data.ResponseBody)
	require.True(t, latest.BodiesOmitted)
}

func TestAuditLog_SessionIDFilterForwarded(t *testing.T) {
	reader := &mockAuditReader{logResult: &auditlog.LogListResult{}}
	h := NewHandler(nil, nil, WithAuditReader(reader))
	c, _ := echotest.Get(t, "/admin/audit/log?session_id=sess-a")
	require.NoError(t, h.AuditLog(c))
	assert.Equal(t, "sess-a", reader.lastQuery.SessionID)
}

// A session_id filter without explicit date parameters queries the whole
// session; explicit dates still apply so the two can be combined.
func TestAuditLog_SessionIDSkipsDefaultDateWindow(t *testing.T) {
	reader := &mockAuditReader{logResult: &auditlog.LogListResult{}}
	h := NewHandler(nil, nil, WithAuditReader(reader))

	c, _ := echotest.Get(t, "/admin/audit/log?session_id=sess-a")
	require.NoError(t, h.AuditLog(c))
	require.True(t, reader.lastQuery.StartDate.IsZero())
	require.True(t, reader.lastQuery.EndDate.IsZero(), "session-only query must be unbounded")

	c, _ = echotest.Get(t, "/admin/audit/log?session_id=sess-a&days=7")
	require.NoError(t, h.AuditLog(c))
	require.False(t, reader.lastQuery.StartDate.IsZero())
	require.False(t, reader.lastQuery.EndDate.IsZero())
}

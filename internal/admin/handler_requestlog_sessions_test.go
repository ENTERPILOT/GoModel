package admin

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/requestlog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestLogSessions_NilReader(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := echotest.Get(t, "/admin/request-logs/sessions")
	require.NoError(t, h.RequestLogSessions(c))
	require.Equal(t, http.StatusOK, rec.Code)

	result := echotest.Decode[requestlog.SessionListResult](t, rec)
	assert.Empty(t, result.Sessions)
	assert.Equal(t, 25, result.Limit)
}

func TestRequestLogSessions_Success(t *testing.T) {
	now := time.Now().UTC()
	reader := &mockRequestLogReader{
		sessionsResult: &requestlog.SessionListResult{
			Sessions: []requestlog.SessionSummary{
				{
					SessionID:    "sess-a",
					RequestCount: 6,
					Latest: requestlog.LogEntry{
						ID:        "log-3",
						Timestamp: now,
						SessionID: "sess-a",
						Provider:  "openai",
						RequestID: "req-3",
					},
				},
				{
					RequestCount: 1,
					Latest: requestlog.LogEntry{
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

	h := NewHandler(nil, nil, WithRequestLogReader(reader))
	before := time.Now().UnixMicro()
	c, rec := echotest.Get(t, "/admin/request-logs/sessions?days=7&user_path=/team")
	require.NoError(t, h.RequestLogSessions(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "/team", reader.lastQuery.UserPath)

	var result struct {
		Sessions []struct {
			RequestCount int                  `json:"request_count"`
			Latest       *requestlog.LogEntry `json:"latest"`
		} `json:"sessions"`
		Total int   `json:"total"`
		AsOf  int64 `json:"as_of"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	require.Equal(t, 2, result.Total)
	assert.GreaterOrEqual(t, result.AsOf, before, "as_of is when the page was read")
	require.Len(t, result.Sessions, 2)
	assert.Equal(t, 6, result.Sessions[0].RequestCount)
	require.NotNil(t, result.Sessions[0].Latest)
	assert.Equal(t, "log-3", result.Sessions[0].Latest.ID)
	assert.Equal(t, 1, result.Sessions[1].RequestCount, "singleton thread")
}

func TestRequestLogSessions_SlimsLatestEntries(t *testing.T) {
	entry := fullRequestLogEntry("log-1")
	entry.SessionID = "sess-a"
	reader := &mockRequestLogReader{
		sessionsResult: &requestlog.SessionListResult{
			Sessions: []requestlog.SessionSummary{{
				SessionID:    "sess-a",
				RequestCount: 2,
				Latest:       entry,
			}},
			Total: 1,
			Limit: 25,
		},
	}
	h := NewHandler(nil, nil, WithRequestLogReader(reader))
	c, rec := echotest.Get(t, "/admin/request-logs/sessions")
	require.NoError(t, h.RequestLogSessions(c))

	var result struct {
		Sessions []struct {
			Latest struct {
				requestlog.LogEntry
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

func TestRequestLog_SessionIDFilterForwarded(t *testing.T) {
	reader := &mockRequestLogReader{logResult: &requestlog.LogListResult{}}
	h := NewHandler(nil, nil, WithRequestLogReader(reader))
	c, _ := echotest.Get(t, "/admin/request-logs?session_id=sess-a")
	require.NoError(t, h.RequestLog(c))
	assert.Equal(t, "sess-a", reader.lastQuery.SessionID)
}

// A session_id filter without explicit date parameters queries the whole
// session; explicit dates still apply so the two can be combined.
func TestRequestLog_SessionIDSkipsDefaultDateWindow(t *testing.T) {
	reader := &mockRequestLogReader{logResult: &requestlog.LogListResult{}}
	h := NewHandler(nil, nil, WithRequestLogReader(reader))

	c, _ := echotest.Get(t, "/admin/request-logs?session_id=sess-a")
	require.NoError(t, h.RequestLog(c))
	require.True(t, reader.lastQuery.StartDate.IsZero())
	require.True(t, reader.lastQuery.EndDate.IsZero(), "session-only query must be unbounded")

	c, _ = echotest.Get(t, "/admin/request-logs?session_id=sess-a&days=7")
	require.NoError(t, h.RequestLog(c))
	require.False(t, reader.lastQuery.StartDate.IsZero())
	require.False(t, reader.lastQuery.EndDate.IsZero())
}

func TestRequestLog_ExcludeOperationFilter(t *testing.T) {
	reader := &mockRequestLogReader{logResult: &requestlog.LogListResult{}}
	h := NewHandler(nil, nil, WithRequestLogReader(reader))

	c, _ := echotest.Get(t, "/admin/request-logs?exclude_operation=mcp,audio_speech")
	require.NoError(t, h.RequestLog(c))
	assert.Equal(t, []core.Operation{core.OperationMCP, core.OperationAudioSpeech}, reader.lastQuery.ExcludeOperations)

	c, rec := echotest.Get(t, "/admin/request-logs?exclude_operation=mcp,nope")
	require.NoError(t, h.RequestLog(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid exclude_operation: nope")
}

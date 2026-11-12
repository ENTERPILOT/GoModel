package admin

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// fullBodyAuditReader serves GetLogs with the bodies loaded, as the list
// handler did before it asked the reader to leave them in the database.
type fullBodyAuditReader struct {
	auditlog.Reader
	omitBodiesSeen bool
}

func (r *fullBodyAuditReader) GetLogs(ctx context.Context, params auditlog.LogQueryParams) (*auditlog.LogListResult, error) {
	r.omitBodiesSeen = r.omitBodiesSeen || params.OmitBodies
	params.OmitBodies = false
	return r.Reader.GetLogs(ctx, params)
}

// listProjectionBody is a captured body, built the way capture builds it.
func listProjectionBody(raw string) any {
	return auditlog.CaptureLoggedBody([]byte(raw))
}

// listProjectionEntries covers every body shape the list projection has to
// classify the way the full-body path does.
func listProjectionEntries(base time.Time) []*auditlog.LogEntry {
	type shape struct {
		path     string
		stream   bool
		request  any
		response any
		data     *auditlog.LogData
	}
	shapes := []shape{
		{path: "/v1/chat/completions",
			request:  listProjectionBody(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`),
			response: listProjectionBody(`{"id":"c1","choices":[{"message":{"content":"hello"}}]}`)},
		{path: "/v1/messages",
			request:  listProjectionBody(`{"model":"claude","messages":[{"role":"user","content":"hi"}]}`),
			response: listProjectionBody(`{"type":"message","content":[{"type":"text","text":"hello"}]}`)},
		{path: "/v1/responses",
			request:  listProjectionBody(`{"model":"gpt-5","input":"hi","instructions":"be brief"}`),
			response: listProjectionBody(`{"id":"r1","output":[{"type":"message","content":[]}]}`)},
		{path: "/p/openai/v1/responses", request: listProjectionBody(`{"input":null}`)},
		{path: "/p/openai/v1/responses", request: listProjectionBody(`{"instructions":"only"}`)},
		{path: "/p/openai/v1/responses", request: listProjectionBody(`{"previous_response_id":"r1"}`)},
		{path: "/p/openai/v1/responses", request: listProjectionBody(`{"previous_response_id":7,"instructions":["x"]}`)},
		{path: "/p/openai/v1/responses", response: listProjectionBody(`{"output":[{"content":null}]}`)},
		{path: "/p/openai/v1/responses", response: listProjectionBody(`{"output":[{"id":"x"}]}`)},
		{path: "/p/openai/v1/responses", response: listProjectionBody(`{"output":[]}`)},
		{path: "/p/openai/v1/responses", response: listProjectionBody(`{"output":["message"]}`)},
		{path: "/p/openai/v1/responses", response: listProjectionBody(`{"output":{"0":{"type":"message"}}}`)},
		{path: "/p/openai/v1/chat", request: listProjectionBody(`{"messages":{"role":"user"}}`),
			response: listProjectionBody(`{"choices":{"0":{}}}`)},
		{path: "/v1/embeddings",
			request:  listProjectionBody(`{"model":"text-embedding-3-small","input":["a","b"]}`),
			response: listProjectionBody(`{"object":"list","data":[{"embedding":[0.1,0.2]}]}`)},
		{path: "/p/openai/v1/embeddings",
			request:  listProjectionBody(`{"model":"text-embedding-3-small","dimensions":256}`),
			response: listProjectionBody(`{"data":[]}`)},
		{path: "/v1/chat/completions",
			request:  listProjectionBody(`{"model":"gpt-5"}`),
			response: listProjectionBody(`{"error":{"message":"bad request","type":"invalid_request_error"}}`)},
		{path: "/p/openai/v1/raw", request: listProjectionBody(`not json`), response: listProjectionBody(`upstream said no`)},
		{path: "/p/openai/v1/raw", request: listProjectionBody(`"{\"messages\":[]}"`),
			response: listProjectionBody(`"{\"choices\":[]}"`)},
		{path: "/p/openai/v1/raw", request: listProjectionBody(`{"messages":[{"role":"user","content":"cut of`)},
		{path: "/p/openai/v1/raw", request: listProjectionBody(`null`), response: listProjectionBody(`null`)},
		{path: "/p/openai/v1/raw", request: listProjectionBody(`[{"messages":[]}]`), response: listProjectionBody(`42`)},
		{path: "/p/openai/v1/raw", request: listProjectionBody(`{}`), response: listProjectionBody(`""`)},
		{path: "/p/openai/v1/raw", request: listProjectionBody(`{"messages":[],"text":"zażółć 漢字 é","n":12345678901234567890}`)},
		{path: "/v1/chat/completions", stream: true,
			request: listProjectionBody(`{"model":"gpt-5","stream":true,"messages":[]}`),
			response: map[string]any{"id": "s1", "object": "chat.completion", "choices": []any{
				map[string]any{"index": float64(0), "message": map[string]any{"role": "assistant", "content": "streamed"}},
			}}},
		{path: "/v1/chat/completions", data: &auditlog.LogData{
			RequestBodyTooBigToHandle:  true,
			ResponseBodyTooBigToHandle: true,
		}},
		{path: "/v1/chat/completions", data: &auditlog.LogData{
			RequestHeaders: map[string]string{"content-type": "application/json"},
			ErrorMessage:   "no bodies captured",
		}},
		{path: "/v1/chat/completions", data: &auditlog.LogData{
			RequestBody: listProjectionBody(`{"messages":[]}`),
			Attempts: []auditlog.AttemptSnapshot{
				{Seq: 1, Kind: auditlog.AttemptKindPrimary, ProviderName: "a", StatusCode: 500, ErrorMessage: "boom",
					ResponseBody:    auditlog.CaptureAttemptResponseBody([]byte(`{"error":{"message":"boom"}}`)),
					ResponseHeaders: map[string]string{"retry-after": "1"}},
				{Seq: 2, Kind: auditlog.AttemptKindRetry, ProviderName: "b", StatusCode: 502,
					ResponseBody: auditlog.CaptureAttemptResponseBody([]byte(`bad gateway`))},
				{Seq: 3, Kind: auditlog.AttemptKindFailover, ProviderName: "c", StatusCode: 200, Success: true},
			},
		}},
		{path: "/v1/chat/completions", data: &auditlog.LogData{
			Attempts: []auditlog.AttemptSnapshot{
				{Seq: 1, Kind: auditlog.AttemptKindPrimary, ProviderName: "a", StatusCode: 429,
					ResponseHeaders: map[string]string{"retry-after": "2"}},
				{Seq: 2, Kind: auditlog.AttemptKindRetry, ProviderName: "b", StatusCode: 200, Success: true},
			},
		}},
		{path: "/v1/chat/completions", data: &auditlog.LogData{
			Attempts: []auditlog.AttemptSnapshot{
				{Seq: 1, Kind: auditlog.AttemptKindPrimary, ProviderName: "a", StatusCode: 200, Success: true},
			},
			Guardrails: []auditlog.GuardrailOutcomeSnapshot{
				{Seq: 1, Phase: "prompt", Instance: "check", Action: "warn", Code: "pii", Detail: map[string]any{"hits": float64(2)}},
			},
		}},
		{path: "/v1/chat/completions", data: &auditlog.LogData{
			RequestRevisions: []auditlog.RequestRevisionSnapshot{
				{Seq: 1, Rewriter: "compress", BytesBefore: 20, BytesAfter: 10,
					Body: listProjectionBody(`{"messages":[]}`)},
			},
		}},
		{path: "/health"},
	}

	entries := make([]*auditlog.LogEntry, 0, len(shapes))
	for i, s := range shapes {
		data := s.data
		if data == nil && (s.request != nil || s.response != nil) {
			data = &auditlog.LogData{RequestBody: s.request, ResponseBody: s.response}
		}
		entries = append(entries, &auditlog.LogEntry{
			ID:             fmt.Sprintf("entry-%02d", i),
			Timestamp:      base.Add(-time.Duration(i) * time.Minute),
			RequestedModel: "gpt-5",
			Provider:       "openai",
			StatusCode:     200,
			RequestID:      fmt.Sprintf("req-%02d", i),
			Method:         http.MethodPost,
			Path:           s.path,
			Stream:         s.stream,
			Data:           data,
		})
	}
	return entries
}

var auditListAsOf = regexp.MustCompile(`"as_of":\d+`)

// The list query leaves bodies in the database; what the list endpoint
// returns must not change because of it.
func TestAuditLogListProjectionMatchesFullBodies(t *testing.T) {
	// conversations is false on MongoDB, whose bodies decode into bson.D and
	// so never qualify in hasConversationPayload, with or without bodies.
	check := func(t *testing.T, store auditlog.LogStore, reader auditlog.Reader, conversations bool) {
		ctx := context.Background()
		entries := listProjectionEntries(time.Now().UTC().Add(-time.Hour))
		require.NoError(t, store.WriteBatch(ctx, entries))

		list := func(reader auditlog.Reader) string {
			h := NewHandler(nil, nil, WithAuditReader(reader))
			c, rec := echotest.Get(t, "/admin/audit/log?days=2&limit=100")
			require.NoError(t, h.AuditLog(c))
			require.Equal(t, http.StatusOK, rec.Code)
			return auditListAsOf.ReplaceAllString(rec.Body.String(), `"as_of":0`)
		}
		full := &fullBodyAuditReader{Reader: reader}
		want := list(full)
		require.True(t, full.omitBodiesSeen, "the list handler must ask for the projection")
		assert.Equal(t, want, list(reader))
		if conversations {
			assert.Contains(t, want, `"conversation_payload":true`)
		}
		assert.Contains(t, want, `"attempts":[`)
		assert.Contains(t, want, `"bodies_omitted":true`)

		projected, err := reader.GetLogs(ctx, auditlog.LogQueryParams{Limit: 100, OmitBodies: true})
		require.NoError(t, err)
		require.Len(t, projected.Entries, len(entries))
		for _, entry := range projected.Entries {
			if entry.Data == nil {
				continue
			}
			for _, body := range []any{entry.Data.RequestBody, entry.Data.ResponseBody} {
				if body != nil {
					assert.IsType(t, auditlog.OmittedBody{}, body, entry.ID)
				}
			}
			for _, attempt := range entry.Data.Attempts {
				if attempt.ResponseBody != nil {
					assert.IsType(t, auditlog.OmittedBody{}, attempt.ResponseBody, entry.ID)
				}
			}
		}
	}

	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := auditlog.NewSQLStore(context.Background(), db, 0)
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		reader, err := auditlog.NewSQLReader(db)
		require.NoError(t, err)
		check(t, store, reader, true)
	})
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		store, err := auditlog.NewMongoDBStore(db, 0)
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		reader, err := auditlog.NewMongoDBReader(db)
		require.NoError(t, err)
		check(t, store, reader, false)
	})
}

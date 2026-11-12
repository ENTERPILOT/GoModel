package admin

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// BenchmarkAuditLogListSQLite serves one 100-row page of the audit list from
// a SQLite file whose entries carry request and response bodies of about the
// given size, plus a failed attempt with an upstream error body.
func BenchmarkAuditLogListSQLite(b *testing.B) {
	for _, size := range []struct {
		name  string
		bytes int
	}{{"body=1.7KB", 1700}, {"body=45KB", 45000}} {
		b.Run(size.name, func(b *testing.B) {
			h := newAuditListBenchHandler(b, size.bytes)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				c, rec := echotest.Get(b, "/admin/audit/log?days=2&limit=100")
				if err := h.AuditLog(c); err != nil || rec.Code != http.StatusOK {
					b.Fatalf("list: %v (status %d)", err, rec.Code)
				}
			}
		})
	}
}

func newAuditListBenchHandler(b *testing.B, bodyBytes int) *Handler {
	b.Helper()
	ctx := context.Background()
	st, err := storage.NewSQLite(storage.SQLiteConfig{Path: filepath.Join(b.TempDir(), "audit.db")})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = st.Close() })
	db, err := sqlx.NewSQLite(st.DB())
	if err != nil {
		b.Fatal(err)
	}
	store, err := auditlog.NewSQLStore(ctx, db, 0)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	reader, err := auditlog.NewSQLReader(db)
	if err != nil {
		b.Fatal(err)
	}

	// Half the size goes to the request, half to the response.
	text := strings.Repeat("lorem ipsum dolor sit amet ", bodyBytes/2/27+1)
	request := fmt.Sprintf(`{"model":"gpt-5","messages":[{"role":"system","content":"be brief"},{"role":"user","content":%q}]}`, text)
	response := fmt.Sprintf(`{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":20}}`, text)

	base := time.Now().UTC().Add(-time.Hour)
	entries := make([]*auditlog.LogEntry, 100)
	for i := range entries {
		entries[i] = &auditlog.LogEntry{
			ID:             fmt.Sprintf("bench-%03d", i),
			Timestamp:      base.Add(-time.Duration(i) * time.Second),
			RequestedModel: "gpt-5",
			ResolvedModel:  "openai/gpt-5",
			Provider:       "openai",
			StatusCode:     200,
			RequestID:      fmt.Sprintf("req-%03d", i),
			Method:         http.MethodPost,
			Path:           "/v1/chat/completions",
			Data: &auditlog.LogData{
				RequestHeaders: map[string]string{"content-type": "application/json"},
				RequestBody:    auditlog.CaptureLoggedBody([]byte(request)),
				ResponseBody:   auditlog.CaptureLoggedBody([]byte(response)),
				Attempts: []auditlog.AttemptSnapshot{
					{Seq: 1, Kind: auditlog.AttemptKindPrimary, ProviderName: "openai", StatusCode: 429,
						ErrorMessage:    "rate limited",
						ResponseBody:    auditlog.CaptureAttemptResponseBody([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`)),
						ResponseHeaders: map[string]string{"retry-after": "1"}},
					{Seq: 2, Kind: auditlog.AttemptKindRetry, ProviderName: "openai", StatusCode: 200, Success: true},
				},
			},
		}
	}
	if err := store.WriteBatch(ctx, entries); err != nil {
		b.Fatal(err)
	}
	return NewHandler(nil, nil, WithAuditReader(reader))
}

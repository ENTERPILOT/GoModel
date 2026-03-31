package auditlog

import (
	"strings"
	"testing"
	"time"
)

func TestBuildAuditLogInsert(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()

	query, args := buildAuditLogInsert([]*LogEntry{
		{
			ID:            "log-1",
			Timestamp:     now,
			DurationNs:    1234,
			Model:         "gpt-4o-mini",
			ResolvedModel: "gpt-4o-mini",
			Provider:      "openai",
			AliasUsed:     true,
			CacheType:     CacheTypeExact,
			StatusCode:    200,
			RequestID:     "req-1",
			AuthKeyID:     "auth-key-1",
			ClientIP:      "127.0.0.1",
			Method:        "POST",
			Path:          "/v1/chat/completions",
			UserPath:      "/team/alpha",
			Stream:        true,
			ErrorType:     "",
			Data: &LogData{
				UserAgent: "test-agent",
			},
		},
		{
			ID:            "log-2",
			Timestamp:     now.Add(time.Second),
			DurationNs:    5678,
			Model:         "gpt-4.1",
			ResolvedModel: "gpt-4.1",
			Provider:      "openai",
			AliasUsed:     false,
			StatusCode:    500,
			RequestID:     "req-2",
			ClientIP:      "10.0.0.1",
			Method:        "POST",
			Path:          "/v1/responses",
			Stream:        false,
			ErrorType:     "server_error",
			Data:          nil,
		},
	})

	normalized := strings.Join(strings.Fields(query), " ")
	wantQuery := "INSERT INTO audit_logs (id, timestamp, duration_ns, model, resolved_model, provider, alias_used, execution_plan_version_id, cache_type, status_code, request_id, auth_key_id, client_ip, method, path, user_path, stream, error_type, data) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19), ($20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30, $31, $32, $33, $34, $35, $36, $37, $38) ON CONFLICT (id) DO NOTHING"
	if normalized != wantQuery {
		t.Fatalf("query = %q, want %q", normalized, wantQuery)
	}

	if got, want := len(args), 38; got != want {
		t.Fatalf("len(args) = %d, want %d", got, want)
	}
	if got := args[0]; got != "log-1" {
		t.Fatalf("args[0] = %v, want log-1", got)
	}
	if got := args[8]; got != CacheTypeExact {
		t.Fatalf("args[8] = %v, want %q", got, CacheTypeExact)
	}
	if got, ok := args[11].(string); !ok || got != "auth-key-1" {
		t.Fatalf("args[11] = (%T) %v, want (string) auth-key-1", args[11], args[11])
	}
	if got, ok := args[15].(string); !ok || got != "/team/alpha" {
		t.Fatalf("args[15] = (%T) %v, want (string) /team/alpha", args[15], args[15])
	}
	if got := args[19]; got != "log-2" {
		t.Fatalf("args[19] = %v, want log-2", got)
	}
	if got, ok := args[30].(string); !ok || got != "" {
		t.Fatalf("args[30] = (%T) %v, want (string) \"\"", args[30], args[30])
	}
	if got, ok := args[34].(string); !ok || got != "/" {
		t.Fatalf("args[34] = (%T) %v, want (string) \"/\"", args[34], args[34])
	}
	if got := string(args[18].([]byte)); got != `{"user_agent":"test-agent"}` {
		t.Fatalf("args[18] = %q, want %q", got, `{"user_agent":"test-agent"}`)
	}
	if got := args[27]; got != nil {
		t.Fatalf("args[27] = %v, want nil cache type", got)
	}
	dataJSON, ok := args[37].([]byte)
	if !ok {
		t.Fatalf("args[37] has type %T, want []byte", args[37])
	}
	if dataJSON != nil {
		t.Fatalf("args[37] = %v, want nil data", dataJSON)
	}
}

func TestAuditLogInsertMaxRowsPerQueryRespectsPostgresLimit(t *testing.T) {
	if got := auditLogInsertMaxRowsPerQuery * auditLogInsertColumnCount; got > postgresMaxBindParameters {
		t.Fatalf("bind parameters = %d, want <= %d", got, postgresMaxBindParameters)
	}
}

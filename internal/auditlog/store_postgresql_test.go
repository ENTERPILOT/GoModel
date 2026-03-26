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
			StatusCode:    200,
			RequestID:     "req-1",
			ClientIP:      "127.0.0.1",
			Method:        "POST",
			Path:          "/v1/chat/completions",
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
	wantQuery := "INSERT INTO audit_logs (id, timestamp, duration_ns, model, resolved_model, provider, alias_used, execution_plan_version_id, status_code, request_id, client_ip, method, path, stream, error_type, data) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16), ($17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30, $31, $32) ON CONFLICT (id) DO NOTHING"
	if normalized != wantQuery {
		t.Fatalf("query = %q, want %q", normalized, wantQuery)
	}

	if got, want := len(args), 32; got != want {
		t.Fatalf("len(args) = %d, want %d", got, want)
	}
	if got := args[0]; got != "log-1" {
		t.Fatalf("args[0] = %v, want log-1", got)
	}
	if got := args[16]; got != "log-2" {
		t.Fatalf("args[16] = %v, want log-2", got)
	}
	if got := string(args[15].([]byte)); got != `{"user_agent":"test-agent"}` {
		t.Fatalf("args[15] = %q, want %q", got, `{"user_agent":"test-agent"}`)
	}
	dataJSON, ok := args[31].([]byte)
	if !ok {
		t.Fatalf("args[31] has type %T, want []byte", args[31])
	}
	if dataJSON != nil {
		t.Fatalf("args[31] = %v, want nil data", dataJSON)
	}
}

func TestAuditLogInsertMaxRowsPerQueryRespectsPostgresLimit(t *testing.T) {
	if got := auditLogInsertMaxRowsPerQuery * auditLogInsertColumnCount; got > postgresMaxBindParameters {
		t.Fatalf("bind parameters = %d, want <= %d", got, postgresMaxBindParameters)
	}
}

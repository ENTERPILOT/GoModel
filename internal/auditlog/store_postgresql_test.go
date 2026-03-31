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
	wantQuery := "INSERT INTO audit_logs (id, timestamp, duration_ns, model, resolved_model, provider, alias_used, execution_plan_version_id, cache_type, status_code, request_id, auth_key_id, auth_method, client_ip, method, path, user_path, stream, error_type, data) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20), ($21, $22, $23, $24, $25, $26, $27, $28, $29, $30, $31, $32, $33, $34, $35, $36, $37, $38, $39, $40) ON CONFLICT (id) DO NOTHING"
	if normalized != wantQuery {
		t.Fatalf("query = %q, want %q", normalized, wantQuery)
	}

	if got, want := len(args), 40; got != want {
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
	if got, ok := args[12].(string); !ok || got != "" {
		t.Fatalf("args[12] = (%T) %v, want (string) \"\"", args[12], args[12])
	}
	if got, ok := args[15].(string); !ok || got != "/v1/chat/completions" {
		t.Fatalf("args[15] = (%T) %v, want (string) /v1/chat/completions", args[15], args[15])
	}
	if got, ok := args[16].(string); !ok || got != "/team/alpha" {
		t.Fatalf("args[16] = (%T) %v, want (string) /team/alpha", args[16], args[16])
	}
	if got := string(args[19].([]byte)); got != `{"user_agent":"test-agent"}` {
		t.Fatalf("args[19] = %q, want %q", got, `{"user_agent":"test-agent"}`)
	}
	if got := args[20]; got != "log-2" {
		t.Fatalf("args[20] = %v, want log-2", got)
	}
	if got, ok := args[31].(string); !ok || got != "" {
		t.Fatalf("args[31] = (%T) %v, want (string) \"\"", args[31], args[31])
	}
	if got, ok := args[32].(string); !ok || got != "" {
		t.Fatalf("args[32] = (%T) %v, want (string) \"\"", args[32], args[32])
	}
	if got := args[28]; got != nil {
		t.Fatalf("args[28] = %v, want nil cache type", got)
	}
	if got, ok := args[36].(string); !ok || got != "/" {
		t.Fatalf("args[36] = (%T) %v, want (string) \"/\"", args[36], args[36])
	}
	dataJSON, ok := args[39].([]byte)
	if !ok {
		t.Fatalf("args[39] has type %T, want []byte", args[39])
	}
	if dataJSON != nil {
		t.Fatalf("args[39] = %v, want nil data", dataJSON)
	}
}

func TestAuditLogInsertMaxRowsPerQueryRespectsPostgresLimit(t *testing.T) {
	if got := auditLogInsertMaxRowsPerQuery * auditLogInsertColumnCount; got > postgresMaxBindParameters {
		t.Fatalf("bind parameters = %d, want <= %d", got, postgresMaxBindParameters)
	}
}

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
			ID:             "log-1",
			Timestamp:      now,
			DurationNs:     1234,
			RequestedModel: "gpt-4o-mini",
			ResolvedModel:  "gpt-4o-mini",
			Provider:       "openai",
			ProviderName:   "primary-openai",
			AliasUsed:      true,
			CacheType:      CacheTypeExact,
			StatusCode:     200,
			RequestID:      "req-1",
			AuthKeyID:      "auth-key-1",
			ClientIP:       "127.0.0.1",
			Method:         "POST",
			Path:           "/v1/chat/completions",
			UserPath:       "/team/alpha",
			Stream:         true,
			ErrorType:      "",
			Data: &LogData{
				UserAgent: "test-agent",
			},
		},
		{
			ID:             "log-2",
			Timestamp:      now.Add(time.Second),
			DurationNs:     5678,
			RequestedModel: "gpt-4.1",
			ResolvedModel:  "gpt-4.1",
			Provider:       "openai",
			AliasUsed:      false,
			StatusCode:     500,
			RequestID:      "req-2",
			ClientIP:       "10.0.0.1",
			Method:         "POST",
			Path:           "/v1/responses",
			Stream:         false,
			ErrorType:      "server_error",
			Data:           nil,
		},
	})

	normalized := strings.Join(strings.Fields(query), " ")
	wantQuery := "INSERT INTO audit_logs (id, timestamp, duration_ns, requested_model, resolved_model, provider, provider_name, alias_used, workflow_version_id, cache_type, status_code, request_id, auth_key_id, auth_method, client_ip, method, path, user_path, stream, error_type, data) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21), ($22, $23, $24, $25, $26, $27, $28, $29, $30, $31, $32, $33, $34, $35, $36, $37, $38, $39, $40, $41, $42) ON CONFLICT (id) DO NOTHING"
	if normalized != wantQuery {
		t.Fatalf("query = %q, want %q", normalized, wantQuery)
	}

	if got, want := len(args), 42; got != want {
		t.Fatalf("len(args) = %d, want %d", got, want)
	}
	if got := args[0]; got != "log-1" {
		t.Fatalf("args[0] = %v, want log-1", got)
	}
	if got := args[6]; got != "primary-openai" {
		t.Fatalf("args[6] = %v, want primary-openai", got)
	}
	if got := args[9]; got != CacheTypeExact {
		t.Fatalf("args[9] = %v, want %q", got, CacheTypeExact)
	}
	if got, ok := args[12].(string); !ok || got != "auth-key-1" {
		t.Fatalf("args[12] = (%T) %v, want (string) auth-key-1", args[12], args[12])
	}
	if got, ok := args[13].(string); !ok || got != "" {
		t.Fatalf("args[13] = (%T) %v, want (string) \"\"", args[13], args[13])
	}
	if got, ok := args[16].(string); !ok || got != "/v1/chat/completions" {
		t.Fatalf("args[16] = (%T) %v, want (string) /v1/chat/completions", args[16], args[16])
	}
	if got, ok := args[17].(string); !ok || got != "/team/alpha" {
		t.Fatalf("args[17] = (%T) %v, want (string) /team/alpha", args[17], args[17])
	}
	if got := string(args[20].([]byte)); got != `{"user_agent":"test-agent"}` {
		t.Fatalf("args[20] = %q, want %q", got, `{"user_agent":"test-agent"}`)
	}
	if got := args[21]; got != "log-2" {
		t.Fatalf("args[21] = %v, want log-2", got)
	}
	if got, ok := args[33].(string); !ok || got != "" {
		t.Fatalf("args[33] = (%T) %v, want (string) \"\"", args[33], args[33])
	}
	if got, ok := args[34].(string); !ok || got != "" {
		t.Fatalf("args[34] = (%T) %v, want (string) \"\"", args[34], args[34])
	}
	if got := args[30]; got != nil {
		t.Fatalf("args[30] = %v, want nil cache type", got)
	}
	if got, ok := args[38].(string); !ok || got != "/" {
		t.Fatalf("args[38] = (%T) %v, want (string) \"/\"", args[38], args[38])
	}
	dataJSON, ok := args[41].([]byte)
	if !ok {
		t.Fatalf("args[41] has type %T, want []byte", args[41])
	}
	if dataJSON != nil {
		t.Fatalf("args[41] = %v, want nil data", dataJSON)
	}
}

func TestAuditLogInsertMaxRowsPerQueryRespectsPostgresLimit(t *testing.T) {
	if got := auditLogInsertMaxRowsPerQuery * auditLogInsertColumnCount; got > postgresMaxBindParameters {
		t.Fatalf("bind parameters = %d, want <= %d", got, postgresMaxBindParameters)
	}
}

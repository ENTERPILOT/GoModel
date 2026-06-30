package auditlog

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type fakePostgreSQLRow struct {
	values []any
}

func (r fakePostgreSQLRow) Scan(dest ...any) error {
	if len(dest) != len(r.values) {
		return fmt.Errorf("scan destination count = %d, want %d", len(dest), len(r.values))
	}
	for i, value := range r.values {
		target := reflect.ValueOf(dest[i])
		if target.Kind() != reflect.Pointer || target.IsNil() {
			return fmt.Errorf("scan destination %d is not a non-nil pointer", i)
		}
		elem := target.Elem()
		if value == nil {
			elem.Set(reflect.Zero(elem.Type()))
			continue
		}
		if elem.Kind() == reflect.Pointer {
			pointerValue := reflect.New(elem.Type().Elem())
			if err := assignScannedValue(pointerValue.Elem(), value); err != nil {
				return fmt.Errorf("scan destination %d: %w", i, err)
			}
			elem.Set(pointerValue)
			continue
		}
		if err := assignScannedValue(elem, value); err != nil {
			return fmt.Errorf("scan destination %d: %w", i, err)
		}
	}
	return nil
}

func assignScannedValue(target reflect.Value, value any) error {
	source := reflect.ValueOf(value)
	if source.Type().AssignableTo(target.Type()) {
		target.Set(source)
		return nil
	}
	if source.Type().ConvertibleTo(target.Type()) {
		target.Set(source.Convert(target.Type()))
		return nil
	}
	return fmt.Errorf("cannot assign %T to %s", value, target.Type())
}

type fakePostgreSQLQueryer struct {
	count int
	rows  pgx.Rows
}

func (q fakePostgreSQLQueryer) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return fakePostgreSQLRow{values: []any{q.count}}
}

func (q fakePostgreSQLQueryer) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	if strings.Contains(sql, "FROM audit_log_attempts") {
		return &fakePostgreSQLRows{read: true}, nil
	}
	if !strings.Contains(sql, "FROM audit_logs") {
		return nil, fmt.Errorf("unexpected query: %s", sql)
	}
	return q.rows, nil
}

type fakePostgreSQLRows struct {
	values []any
	read   bool
	closed bool
	err    error
}

func (r *fakePostgreSQLRows) Close() {
	r.closed = true
}

func (r *fakePostgreSQLRows) Err() error {
	return r.err
}

func (r *fakePostgreSQLRows) CommandTag() pgconn.CommandTag {
	return pgconn.CommandTag{}
}

func (r *fakePostgreSQLRows) FieldDescriptions() []pgconn.FieldDescription {
	return nil
}

func (r *fakePostgreSQLRows) Next() bool {
	if r.read {
		r.Close()
		return false
	}
	r.read = true
	return true
}

func (r *fakePostgreSQLRows) Scan(dest ...any) error {
	return fakePostgreSQLRow{values: r.values}.Scan(dest...)
}

func (r *fakePostgreSQLRows) Values() ([]any, error) {
	return r.values, nil
}

func (r *fakePostgreSQLRows) RawValues() [][]byte {
	return nil
}

func (r *fakePostgreSQLRows) Conn() *pgx.Conn {
	return nil
}

func postgreSQLAuditLogRowValues(errorType any) []any {
	return []any{
		"entry-null-error-type",
		time.Unix(1700000000, 0).UTC(),
		int64(1234),
		"gpt-4o-mini",
		"gpt-4o-mini",
		"openai",
		"primary-openai",
		false,
		nil,
		nil,
		200,
		"req-1",
		nil,
		"master_key",
		"127.0.0.1",
		"POST",
		"/v1/chat/completions",
		"/",
		false,
		errorType,
		`{"user_agent":"test-agent"}`,
	}
}

func TestPostgreSQLReaderGetLogsAllowsNullErrorType(t *testing.T) {
	rows := &fakePostgreSQLRows{values: postgreSQLAuditLogRowValues(nil)}
	reader := &PostgreSQLReader{
		pool: fakePostgreSQLQueryer{
			count: 1,
			rows:  rows,
		},
	}

	result, err := reader.GetLogs(context.Background(), LogQueryParams{Limit: 10})
	if err != nil {
		t.Fatalf("GetLogs failed: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("Total = %d, want 1", result.Total)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("len(Entries) = %d, want 1", len(result.Entries))
	}
	entry := result.Entries[0]
	if entry.ErrorType != "" {
		t.Fatalf("ErrorType = %q, want empty", entry.ErrorType)
	}
	if entry.ProviderName != "primary-openai" {
		t.Fatalf("ProviderName = %q, want primary-openai", entry.ProviderName)
	}
	if entry.Data == nil || entry.Data.UserAgent != "test-agent" {
		t.Fatalf("Data = %#v, want user_agent", entry.Data)
	}
	if !rows.closed {
		t.Fatal("rows were not closed")
	}
}

func TestScanPostgreSQLLogEntryAllowsNullErrorType(t *testing.T) {
	entry, err := scanPostgreSQLLogEntry(fakePostgreSQLRow{values: postgreSQLAuditLogRowValues(nil)})
	if err != nil {
		t.Fatalf("scanPostgreSQLLogEntry failed: %v", err)
	}
	if entry.ErrorType != "" {
		t.Fatalf("ErrorType = %q, want empty", entry.ErrorType)
	}
	if entry.ProviderName != "primary-openai" {
		t.Fatalf("ProviderName = %q, want primary-openai", entry.ProviderName)
	}
	if entry.AuthMethod != "master_key" {
		t.Fatalf("AuthMethod = %q, want master_key", entry.AuthMethod)
	}
	if entry.Data == nil || entry.Data.UserAgent != "test-agent" {
		t.Fatalf("Data = %#v, want user_agent", entry.Data)
	}
}

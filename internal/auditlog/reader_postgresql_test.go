package auditlog

import (
	"fmt"
	"reflect"
	"testing"
	"time"
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

func TestScanPostgreSQLLogEntryAllowsNullErrorType(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()

	entry, err := scanPostgreSQLLogEntry(fakePostgreSQLRow{values: []any{
		"entry-null-error-type",
		now,
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
		nil,
		`{"user_agent":"test-agent"}`,
	}})
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

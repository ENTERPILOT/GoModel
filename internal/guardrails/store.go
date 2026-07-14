package guardrails

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/enterpilot/gomodel/internal/validation"
)

// ErrNotFound indicates a requested guardrail was not found.
var ErrNotFound = errors.New("guardrail not found")

// ValidationError indicates invalid guardrail input or state.
type ValidationError = validation.Error

func newValidationError(message string, err error) error {
	return validation.NewError(message, err)
}

// IsValidationError reports whether err is a validation error.
func IsValidationError(err error) bool {
	return validation.IsError(err)
}

// Store defines persistence operations for reusable guardrail definitions.
type Store interface {
	List(ctx context.Context) ([]Definition, error)
	Get(ctx context.Context, name string) (*Definition, error)
	Upsert(ctx context.Context, definition Definition) error
	UpsertMany(ctx context.Context, definitions []Definition) error
	Delete(ctx context.Context, name string) error
	Close() error
}

type definitionScanner interface {
	Scan(dest ...any) error
}

type definitionRows interface {
	definitionScanner
	Next() bool
	Err() error
}

func normalizeDefinitionName(name string) string {
	return strings.TrimSpace(name)
}

func collectDefinitions(rows definitionRows, scan func(definitionScanner) (Definition, error)) ([]Definition, error) {
	result := make([]Definition, 0)
	for rows.Next() {
		definition, err := scan(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, definition)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func nullableString(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func nullableStringValue(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return strings.TrimSpace(value.String)
}

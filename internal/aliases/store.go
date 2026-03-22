package aliases

import (
	"context"
	"errors"
	"strings"
)

// ErrNotFound indicates a requested alias was not found.
var ErrNotFound = errors.New("alias not found")

// ValidationError indicates invalid alias input or invalid alias state.
type ValidationError struct {
	Message string
	Err     error
}

func (e *ValidationError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *ValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func newValidationError(message string, err error) error {
	return &ValidationError{Message: message, Err: err}
}

// IsValidationError reports whether err is a validation error.
func IsValidationError(err error) bool {
	_, ok := errors.AsType[*ValidationError](err)
	return ok
}

// Store defines persistence operations for aliases.
type Store interface {
	List(ctx context.Context) ([]Alias, error)
	Get(ctx context.Context, name string) (*Alias, error)
	Upsert(ctx context.Context, alias Alias) error
	Delete(ctx context.Context, name string) error
	Close() error
}

type aliasScanner interface {
	Scan(dest ...any) error
}

type aliasRows interface {
	aliasScanner
	Next() bool
	Err() error
}

func normalizeName(name string) string {
	return strings.TrimSpace(name)
}

func normalizeAlias(alias Alias) (Alias, error) {
	alias.Name = normalizeName(alias.Name)
	alias.TargetModel = strings.TrimSpace(alias.TargetModel)
	alias.TargetProvider = strings.TrimSpace(alias.TargetProvider)
	alias.Description = strings.TrimSpace(alias.Description)

	if alias.Name == "" {
		return Alias{}, newValidationError("alias name is required", nil)
	}
	if alias.TargetModel == "" {
		return Alias{}, newValidationError("target_model is required", nil)
	}
	if _, err := alias.TargetSelector(); err != nil {
		return Alias{}, newValidationError("invalid target selector: "+err.Error(), err)
	}
	return alias, nil
}

func collectAliases(rows aliasRows, scan func(aliasScanner) (Alias, error)) ([]Alias, error) {
	result := make([]Alias, 0)
	for rows.Next() {
		alias, err := scan(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, alias)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

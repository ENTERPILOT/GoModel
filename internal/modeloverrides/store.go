package modeloverrides

import (
	"context"
	"errors"
	"fmt"
)

// ErrNotFound indicates a requested override was not found.
var ErrNotFound = errors.New("model override not found")

// ValidationError indicates invalid override input or invalid override state.
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
	var target *ValidationError
	return errors.As(err, &target)
}

// Store defines persistence operations for model overrides.
type Store interface {
	List(ctx context.Context) ([]Override, error)
	Upsert(ctx context.Context, override Override) error
	Delete(ctx context.Context, selector string) error
	Close() error
}

func collectOverrides(next func() (Override, bool, error), rowsErr func() error) ([]Override, error) {
	result := make([]Override, 0)
	for {
		override, ok, err := next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		result = append(result, override)
	}
	if err := rowsErr(); err != nil {
		return nil, fmt.Errorf("iterate model overrides: %w", err)
	}
	return result, nil
}

package workflows

import (
	"context"
	"errors"
)

// ErrNotFound indicates a requested workflow version was not found.
var ErrNotFound = errors.New("workflow version not found")

// ValidationError indicates invalid workflow input or state.
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

// Store defines persistence operations for immutable workflow versions.
type Store interface {
	ListActive(ctx context.Context) ([]Version, error)
	Get(ctx context.Context, id string) (*Version, error)
	Create(ctx context.Context, input CreateInput) (*Version, error)
	EnsureManagedDefaultGlobal(ctx context.Context, input CreateInput, workflowHash string) (*Version, error)
	Deactivate(ctx context.Context, id string) error
	Close() error
}

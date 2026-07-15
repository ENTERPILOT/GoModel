package workflows

import (
	"context"
	"errors"

	"github.com/enterpilot/gomodel/internal/validation"
)

// ErrNotFound indicates a requested workflow version was not found.
var ErrNotFound = errors.New("workflow version not found")

// ValidationError indicates invalid workflow input or state.
type ValidationError = validation.Error

func newValidationError(message string, err error) error {
	return validation.NewError(message, err)
}

// IsValidationError reports whether err is a validation error.
func IsValidationError(err error) bool {
	return validation.IsError(err)
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

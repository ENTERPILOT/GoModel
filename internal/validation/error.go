// Package validation provides the shared validation error type used by
// admin-managed resources (aliases, auth keys, guardrails, workflows,
// model selectors and overrides).
package validation

import "errors"

// Error indicates invalid user-supplied input or invalid resource state.
type Error struct {
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// NewError creates a validation error.
func NewError(message string, err error) error {
	return &Error{Message: message, Err: err}
}

// IsError reports whether err is a validation error.
func IsError(err error) bool {
	_, ok := errors.AsType[*Error](err)
	return ok
}

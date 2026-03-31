package authkeys

import (
	"context"
	"errors"
	"strings"
	"time"
)

var (
	// ErrNotFound indicates a requested auth key record does not exist.
	ErrNotFound = errors.New("auth key not found")
	// ErrInvalidToken indicates the presented token does not match a known key.
	ErrInvalidToken = errors.New("invalid API key")
	// ErrInactive indicates the presented token belongs to an inactive key.
	ErrInactive = errors.New("API key is inactive")
	// ErrExpired indicates the presented token belongs to an expired key.
	ErrExpired = errors.New("API key expired")
)

// ValidationError indicates invalid auth key input or state.
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

// Store defines persistence operations for managed auth keys.
type Store interface {
	List(ctx context.Context) ([]AuthKey, error)
	Create(ctx context.Context, key AuthKey) error
	Deactivate(ctx context.Context, id string, now time.Time) error
	Close() error
}

type authKeyScanner interface {
	Scan(dest ...any) error
}

type authKeyRows interface {
	authKeyScanner
	Next() bool
	Err() error
}

func normalizeCreateInput(input CreateInput) (CreateInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if input.Name == "" {
		return CreateInput{}, newValidationError("name is required", nil)
	}
	if input.ExpiresAt != nil {
		expiresAt := input.ExpiresAt.UTC()
		now := time.Now().UTC()
		if !expiresAt.After(now) {
			return CreateInput{}, newValidationError("expires_at must be in the future", nil)
		}
		input.ExpiresAt = &expiresAt
	}
	return input, nil
}

func normalizeID(id string) string {
	return strings.TrimSpace(id)
}

func collectAuthKeys(rows authKeyRows, scan func(authKeyScanner) (AuthKey, error)) ([]AuthKey, error) {
	result := make([]AuthKey, 0)
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

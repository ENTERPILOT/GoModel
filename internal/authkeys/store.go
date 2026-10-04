package authkeys

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/validation"
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
	// ErrAlreadyImported indicates a key with the imported token hash exists.
	ErrAlreadyImported = errors.New("an auth key with this secret_hash already exists")
)

// ValidationError indicates invalid auth key input or state.
type ValidationError = validation.Error

func newValidationError(message string, err error) error {
	return validation.NewError(message, err)
}

// IsValidationError reports whether err is a validation error.
func IsValidationError(err error) bool {
	return validation.IsError(err)
}

// Store defines persistence operations for managed auth keys.
type Store interface {
	List(ctx context.Context) ([]AuthKey, error)
	Create(ctx context.Context, key AuthKey) error
	UpdateLabels(ctx context.Context, id string, labels []string, now time.Time) error
	UpdateAllowedModels(ctx context.Context, id string, allowedModels []string, now time.Time) error
	UpdateDashboardAccess(ctx context.Context, id string, allowed bool, now time.Time) error
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
	userPath, err := core.NormalizeUserPath(input.UserPath)
	if err != nil {
		return CreateInput{}, newValidationError("invalid user_path", err)
	}
	input.UserPath = userPath
	input.Labels = core.MergeLabels(input.Labels)
	input.AllowedModels = NormalizeAllowedModels(input.AllowedModels)
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

// liteLLMRedactedValue matches LiteLLM's abbreviated key_name, such as
// "sk-...abcd", so a pasted token is never stored where the dashboard shows it.
var liteLLMRedactedValue = regexp.MustCompile(`^sk-\.\.\.\S{0,8}$`)

func normalizeImportInput(input ImportInput) (ImportInput, error) {
	createInput, err := normalizeCreateInput(input.CreateInput)
	if err != nil {
		return ImportInput{}, err
	}
	input.CreateInput = createInput
	input.ImportedFrom = strings.TrimSpace(input.ImportedFrom)
	if input.ImportedFrom != ImportedFromLiteLLM {
		return ImportInput{}, newValidationError(`imported_from must be "`+ImportedFromLiteLLM+`"`, nil)
	}
	input.SecretHash = strings.ToLower(strings.TrimSpace(input.SecretHash))
	if !isSHA256Hex(input.SecretHash) {
		return ImportInput{}, newValidationError("secret_hash must be the 64-character hex SHA-256 of the token", nil)
	}
	input.RedactedValue = strings.TrimSpace(input.RedactedValue)
	if input.RedactedValue == "" {
		input.RedactedValue = liteLLMTokenPrefix + "..."
	}
	if !liteLLMRedactedValue.MatchString(input.RedactedValue) {
		return ImportInput{}, newValidationError(`redacted_value must look like "sk-...abcd"`, nil)
	}
	return input, nil
}

func isSHA256Hex(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// NormalizeAllowedModels trims, drops empty entries, and de-duplicates model
// selectors while preserving order. Selector syntax is validated by the caller
// that owns the provider catalog.
func NormalizeAllowedModels(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, dup := seen[value]; dup {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if len(result) == 0 {
		return nil
	}
	return result
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

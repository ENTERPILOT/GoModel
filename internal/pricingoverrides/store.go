package pricingoverrides

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/goccy/go-json"

	"gomodel/internal/modelselectors"
)

// ErrNotFound indicates a requested pricing override was not found.
var ErrNotFound = errors.New("model pricing override not found")

// ValidationError indicates invalid override input or invalid override state.
type ValidationError = modelselectors.ValidationError

func newValidationError(message string, err error) error {
	return modelselectors.NewValidationError(message, err)
}

// IsValidationError reports whether err is a validation error.
func IsValidationError(err error) bool {
	return modelselectors.IsValidationError(err)
}

// Store defines persistence operations for pricing overrides.
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
		return nil, fmt.Errorf("iterate model pricing overrides: %w", err)
	}
	return result, nil
}

func prepareOverrideUpsert(override Override) (Override, string, error) {
	override, err := normalizeStoredOverride(override)
	if err != nil {
		return Override{}, "", err
	}

	pricingJSON, err := json.Marshal(override.Pricing)
	if err != nil {
		return Override{}, "", fmt.Errorf("encode pricing: %w", err)
	}

	now := time.Now().UTC()
	if override.CreatedAt.IsZero() {
		override.CreatedAt = now
	}
	override.UpdatedAt = now
	return override, string(pricingJSON), nil
}

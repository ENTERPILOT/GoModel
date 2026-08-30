// Package users manages per-user-path model access policies. A user path is
// the hierarchy behind every managed API key (/team/alpha/alice): interior
// nodes act as groups, leaves as users. Each node may carry an allowlist of
// model selectors; a request must satisfy every non-empty allowlist between
// its key, its user path, and the root, so a child can narrow but never widen
// what its group permits.
package users

import (
	"context"
	"errors"
	"time"

	"github.com/enterpilot/gomodel/internal/validation"
)

// User is one persisted access policy keyed by user path.
type User struct {
	UserPath string `json:"user_path" bson:"_id"`
	// AllowedModels lists canonical model selectors: exact "provider/model",
	// provider-wide "provider/", or model-wide "model". Empty means the node
	// itself does not restrict models.
	AllowedModels []string  `json:"allowed_models" bson:"allowed_models"`
	Description   string    `json:"description,omitempty" bson:"description,omitempty"`
	CreatedAt     time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt     time.Time `json:"updated_at" bson:"updated_at"`
	// Managed marks a row declared in config.yaml / USERS. Managed rows shadow
	// store rows of the same path and are read-only through the admin API.
	Managed bool `json:"managed,omitempty" bson:"-"`
}

var (
	// ErrNotFound indicates the requested user path has no policy.
	ErrNotFound = errors.New("user not found")
	// ErrManaged indicates the user path is declared in config and cannot be
	// changed through the admin API.
	ErrManaged = errors.New("user is managed by configuration")
)

// ValidationError indicates invalid user input.
type ValidationError = validation.Error

func newValidationError(message string, err error) error {
	return validation.NewError(message, err)
}

// IsValidationError reports whether err is a validation error.
func IsValidationError(err error) bool {
	return validation.IsError(err)
}

// Store defines persistence operations for user policies.
type Store interface {
	List(ctx context.Context) ([]User, error)
	Upsert(ctx context.Context, user User) error
	Delete(ctx context.Context, userPath string) error
	Close() error
}

// Catalog is the configured-provider surface needed to validate selectors.
type Catalog interface {
	ProviderNames() []string
}

// Package users manages per-user-path policies. A user path is the hierarchy
// behind every managed API key (/team/alpha/alice): interior nodes act as
// groups, leaves as users. Each node may carry an allowlist of model
// selectors; a request must satisfy every non-empty allowlist between its
// key, its user path, and the root, so a child can narrow but never widen
// what its group permits. A node may also carry a request policy (a default
// model and ceilings on reasoning effort and output tokens) that the paths
// below it inherit; see RequestPolicy.
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
	AllowedModels []string `json:"allowed_models" bson:"allowed_models"`
	// DefaultModel serves a chat, Responses or Messages request under this
	// path that names neither a model nor a provider. It is resolved like a
	// model the client sent, so it may name a virtual model. The nearest path
	// that sets one wins.
	DefaultModel string `json:"default_model,omitempty" bson:"default_model,omitempty"`
	// MaxReasoningEffort is the highest reasoning effort requests under this
	// path may send upstream, one of requestparams.ReasoningEffortLevels.
	// The lowest ceiling between the path and the root applies. A model that
	// cannot reason below a minimum level keeps that minimum, and batches and
	// /p/ passthrough are not capped.
	MaxReasoningEffort string `json:"max_reasoning_effort,omitempty" bson:"max_reasoning_effort,omitempty"`
	// MaxOutputTokens is the highest output token limit requests under this
	// path may send upstream, and the limit of a request that sends none;
	// zero sets no ceiling. The lowest ceiling between the path and the root
	// applies. Batches and /p/ passthrough are not capped.
	MaxOutputTokens int       `json:"max_output_tokens,omitempty" bson:"max_output_tokens,omitempty"`
	Description     string    `json:"description,omitempty" bson:"description,omitempty"`
	CreatedAt       time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt       time.Time `json:"updated_at" bson:"updated_at"`
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

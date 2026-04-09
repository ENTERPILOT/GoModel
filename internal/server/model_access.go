package server

import (
	"context"

	"gomodel/internal/core"
)

// RequestModelAuthorizer validates request-scoped access to concrete models.
type RequestModelAuthorizer interface {
	ValidateModelAccess(ctx context.Context, selector core.ModelSelector) error
	AllowsModel(ctx context.Context, selector core.ModelSelector) bool
	FilterPublicModels(ctx context.Context, models []core.Model) []core.Model
}

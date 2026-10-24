package virtualmodels

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/enterpilot/gomodel/internal/users"
)

// A model-less request acts on the whole provider account, so a key passes
// only when it is granted the whole provider, never through one of its models.
func TestService_ValidateProviderAccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	userStore, err := users.NewSQLStore(ctx, sqlxtest.NewSQLite(t))
	require.NoError(t, err)
	policy, err := users.NewService(userStore, testCatalog())
	require.NoError(t, err)
	svc := newTestService(t)
	svc.SetAccessPolicy(policy)

	tests := []struct {
		name     string
		allowed  []string
		provider string
		wantErr  bool
	}{
		{name: "unrestricted key", provider: "anthropic"},
		{name: "global grant", allowed: []string{"/"}, provider: "anthropic"},
		{name: "provider-wide grant", allowed: []string{"anthropic/"}, provider: "anthropic"},
		{name: "model on another provider", allowed: []string{"openai/gpt-4o-mini"}, provider: "anthropic", wantErr: true},
		{name: "model on the same provider", allowed: []string{"openai/gpt-4o-mini"}, provider: "openai", wantErr: true},
		{name: "model-wide grant", allowed: []string{"gpt-4o-mini"}, provider: "openai", wantErr: true},
		{name: "unknown provider", allowed: []string{"openai/"}, provider: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqCtx := core.WithCredentialAllowedModels(ctx, tt.allowed)
			err := svc.ValidateProviderAccess(reqCtx, tt.provider)
			if !tt.wantErr {
				assert.NoError(t, err)
				return
			}
			gatewayErr, ok := err.(*core.GatewayError)
			require.True(t, ok, "err = %v", err)
			require.NotNil(t, gatewayErr.Code)
			assert.Equal(t, "model_access_denied", *gatewayErr.Code)
		})
	}
}

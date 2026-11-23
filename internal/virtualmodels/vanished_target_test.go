package virtualmodels

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// A target can go missing after its virtual model was stored: the model was
// deprecated or its provider removed. Writes that keep such a target must
// still succeed, so the virtual model can be disabled, renamed, or edited;
// only a target the write introduces is checked against the catalog.
func TestService_StoredTargetThatVanishedDoesNotBlockWrites(t *testing.T) {
	t.Parallel()
	catalog := fakeCatalog{
		providers: []string{"openai", "legacy"},
		supported: map[string]core.Model{
			"openai/gpt-4o":      {ID: "openai/gpt-4o"},
			"openai/gpt-4o-mini": {ID: "openai/gpt-4o-mini"},
			"legacy/old-model":   {ID: "legacy/old-model"},
		},
	}
	svc, err := NewService(newSQLVMStore(t), catalog, true)
	require.NoError(t, err)
	ctx := context.Background()

	targets := []Target{
		{Provider: "openai", Model: "gpt-4o"},
		{Provider: "legacy", Model: "old-model"},
	}
	require.NoError(t, svc.Upsert(ctx, VirtualModel{Source: "cheap", Targets: targets, Enabled: true}))

	// The provider and its model disappear from the catalog.
	delete(catalog.supported, "legacy/old-model")
	catalog.providers = []string{"openai"}
	svc.catalog = catalog

	tests := []struct {
		name    string
		write   func() error
		wantErr string
	}{
		{
			name: "disable",
			write: func() error {
				return svc.Upsert(ctx, VirtualModel{Source: "cheap", Targets: targets, Enabled: false})
			},
		},
		{
			name: "enable again",
			write: func() error {
				return svc.Upsert(ctx, VirtualModel{Source: "cheap", Targets: targets, Enabled: true})
			},
		},
		{
			name: "add an available target",
			write: func() error {
				next := append([]Target{{Provider: "openai", Model: "gpt-4o-mini"}}, targets...)
				return svc.Upsert(ctx, VirtualModel{Source: "cheap", Targets: next, Enabled: true})
			},
		},
		{
			name: "add a missing model",
			write: func() error {
				next := append([]Target{{Provider: "openai", Model: "gpt-9"}}, targets...)
				return svc.Upsert(ctx, VirtualModel{Source: "cheap", Targets: next, Enabled: true})
			},
			wantErr: "target model not found: openai/gpt-9",
		},
		{
			name: "add an unknown provider",
			write: func() error {
				next := append([]Target{{Provider: "nope", Model: "gpt-9"}}, targets...)
				return svc.Upsert(ctx, VirtualModel{Source: "cheap", Targets: next, Enabled: true})
			},
			wantErr: "nope",
		},
		{
			// Moving the provider between the provider field and the model
			// text changes what the target may route to, so it is checked
			// as a new target.
			name: "respell the missing target",
			write: func() error {
				next := []Target{targets[0], {Model: "legacy/old-model"}}
				return svc.Upsert(ctx, VirtualModel{Source: "cheap", Targets: next, Enabled: true})
			},
			wantErr: "target model not found: legacy/old-model",
		},
		{
			name: "rename",
			write: func() error {
				return svc.Rename(ctx, "cheap", VirtualModel{Source: "budget", Targets: targets, Enabled: true})
			},
		},
	}
	// The writes build on each other, so the subtests run in order.
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.write()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}

	renamed, ok := svc.Get("budget")
	require.True(t, ok)
	assert.Equal(t, targets, renamed.Targets)
}

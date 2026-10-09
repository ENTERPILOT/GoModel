package virtualmodels

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func limitsModel(id string, contextWindow, maxOutput *int) core.Model {
	model := core.Model{ID: id, Object: "model", OwnedBy: "openai"}
	if contextWindow != nil || maxOutput != nil {
		model.Metadata = &core.ModelMetadata{ContextWindow: contextWindow, MaxOutputTokens: maxOutput}
	}
	return model
}

func newLimitsService(t *testing.T) (*Service, fakeCatalog) {
	t.Helper()
	catalog := fakeCatalog{
		providers: []string{"openai"},
		supported: map[string]core.Model{
			"openai/large":   limitsModel("openai/large", new(200000), new(8000)),
			"openai/small":   limitsModel("openai/small", new(128000), new(16000)),
			"openai/unknown": limitsModel("openai/unknown", nil, nil),
		},
	}
	svc, err := NewService(newSQLVMStore(t), catalog, true)
	require.NoError(t, err)
	return svc, catalog
}

func exposedModel(t *testing.T, svc *Service, id string) core.Model {
	t.Helper()
	for _, model := range svc.ExposedModels() {
		if model.ID == id {
			return model
		}
	}
	require.Failf(t, "model not exposed", "ExposedModels() has no %q", id)
	return core.Model{}
}

func TestExposedModels_TokenLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		rows        []VirtualModel
		disable     string
		wantContext *int
		wantOutput  *int
	}{
		{
			name: "smallest value across targets",
			rows: []VirtualModel{{Source: "vm", Targets: []Target{
				{Model: "openai/large"}, {Model: "openai/small"}, {Model: "openai/unknown"},
			}}},
			wantContext: new(128000),
			wantOutput:  new(8000),
		},
		{
			name: "configured value wins per field",
			rows: []VirtualModel{{
				Source:        "vm",
				Targets:       []Target{{Model: "openai/large"}, {Model: "openai/small"}},
				ContextWindow: new(1000000),
			}},
			wantContext: new(1000000),
			wantOutput:  new(8000),
		},
		{
			name: "chained virtual model applies its own limits",
			rows: []VirtualModel{
				{Source: "inner", Targets: []Target{{Model: "openai/large"}}, ContextWindow: new(32000)},
				{Source: "vm", Targets: []Target{{Model: "inner"}, {Model: "openai/small"}}},
			},
			wantContext: new(32000),
			wantOutput:  new(8000),
		},
		{
			name: "disabled chained virtual model is ignored",
			rows: []VirtualModel{
				{Source: "inner", Targets: []Target{{Model: "openai/large"}}, ContextWindow: new(32000)},
				{Source: "vm", Targets: []Target{{Model: "inner"}, {Model: "openai/small"}}},
			},
			disable:     "inner",
			wantContext: new(128000),
			wantOutput:  new(16000),
		},
		{
			name: "no target reports a value",
			rows: []VirtualModel{{Source: "vm", Targets: []Target{{Model: "openai/unknown"}}}},
		},
		{
			name: "configured value on a target without metadata",
			rows: []VirtualModel{{
				Source:          "vm",
				Targets:         []Target{{Model: "openai/unknown"}},
				MaxOutputTokens: new(4096),
			}},
			wantOutput: new(4096),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc, catalog := newLimitsService(t)
			ctx := context.Background()
			for _, row := range tt.rows {
				row.Enabled = true
				require.NoError(t, svc.Upsert(ctx, row))
			}
			if tt.disable != "" {
				row, ok := svc.Get(tt.disable)
				require.True(t, ok)
				row.Enabled = false
				require.NoError(t, svc.Upsert(ctx, *row))
			}

			model := exposedModel(t, svc, "vm")
			if tt.wantContext == nil && tt.wantOutput == nil {
				if model.Metadata != nil {
					assert.Nil(t, model.Metadata.ContextWindow)
					assert.Nil(t, model.Metadata.MaxOutputTokens)
				}
			} else {
				require.NotNil(t, model.Metadata)
				assert.Equal(t, tt.wantContext, model.Metadata.ContextWindow)
				assert.Equal(t, tt.wantOutput, model.Metadata.MaxOutputTokens)
			}

			// Listing never writes through to the catalog's shared metadata.
			assert.Equal(t, new(200000), catalog.supported["openai/large"].Metadata.ContextWindow)
			assert.Equal(t, new(8000), catalog.supported["openai/large"].Metadata.MaxOutputTokens)
		})
	}
}

func TestTokenLimitsValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		vm      VirtualModel
		wantErr string
	}{
		{
			name:    "zero context window",
			vm:      VirtualModel{Source: "vm", Targets: []Target{{Model: "openai/small"}}, ContextWindow: new(0)},
			wantErr: "context_window must be a positive number of tokens",
		},
		{
			name:    "negative max output tokens",
			vm:      VirtualModel{Source: "vm", Targets: []Target{{Model: "openai/small"}}, MaxOutputTokens: new(-1)},
			wantErr: "max_output_tokens must be a positive number of tokens",
		},
		{
			name:    "access policy",
			vm:      VirtualModel{Source: "openai/small", ContextWindow: new(1000)},
			wantErr: "can only be configured for a redirect",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc, _ := newLimitsService(t)
			tt.vm.Enabled = true
			err := svc.Upsert(context.Background(), tt.vm)
			require.Error(t, err)
			assert.True(t, IsValidationError(err))
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

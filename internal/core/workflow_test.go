package core

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewWorkflowSelector_DropsInvalidUserPath(t *testing.T) {
	t.Parallel()

	selector := NewWorkflowSelector("openai", "gpt-5", "/team/../alpha")
	require.Empty(t, selector.UserPath)
}

func TestWorkflowFeaturesApplyUpperBound_DisablesBudgetWhenUsageDisabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		base       WorkflowFeatures
		caps       WorkflowFeatures
		wantUsage  bool
		wantBudget bool
	}{
		{
			name:       "disables budget when usage cap disabled",
			base:       WorkflowFeatures{Usage: true, Budget: true},
			caps:       WorkflowFeatures{Usage: false, Budget: true},
			wantUsage:  false,
			wantBudget: false,
		},
		{
			name:       "keeps budget when usage and budget enabled",
			base:       WorkflowFeatures{Usage: true, Budget: true},
			caps:       WorkflowFeatures{Usage: true, Budget: true},
			wantUsage:  true,
			wantBudget: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			features := tt.base.ApplyUpperBound(tt.caps)
			require.Equal(t, tt.wantUsage, features.Usage)
			require.Equal(t, tt.wantBudget, features.Budget)
		})
	}
}

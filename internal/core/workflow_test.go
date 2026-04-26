package core

import "testing"

func TestNewWorkflowSelector_DropsInvalidUserPath(t *testing.T) {
	t.Parallel()

	selector := NewWorkflowSelector("openai", "gpt-5", "/team/../alpha")
	if selector.UserPath != "" {
		t.Fatalf("UserPath = %q, want empty", selector.UserPath)
	}
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
			if features.Usage != tt.wantUsage {
				t.Fatalf("ApplyUpperBound().Usage = %v, want %v", features.Usage, tt.wantUsage)
			}
			if features.Budget != tt.wantBudget {
				t.Fatalf("ApplyUpperBound().Budget = %v, want %v", features.Budget, tt.wantBudget)
			}
		})
	}
}

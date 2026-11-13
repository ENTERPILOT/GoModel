package anthropicapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestThinkingBudgetExceeds(t *testing.T) {
	tests := []struct {
		name     string
		thinking *Thinking
		ceiling  string
		want     bool
	}{
		{name: "no thinking", ceiling: "low"},
		{name: "no ceiling", thinking: &Thinking{Type: "enabled", BudgetTokens: 63000}},
		{name: "disabled thinking", thinking: &Thinking{Type: "disabled"}, ceiling: "none"},
		{name: "adaptive thinking has no budget", thinking: &Thinking{Type: "adaptive"}, ceiling: "low"},
		{name: "above the high budget", thinking: &Thinking{Type: "enabled", BudgetTokens: 63000}, ceiling: "high", want: true},
		{name: "at the high budget", thinking: &Thinking{Type: "enabled", BudgetTokens: 20000}, ceiling: "high"},
		{name: "above the low budget", thinking: &Thinking{Type: "enabled", BudgetTokens: 9999}, ceiling: "low", want: true},
		{name: "within the low budget", thinking: &Thinking{Type: "enabled", BudgetTokens: 2000}, ceiling: " LOW "},
		{name: "a none ceiling allows no budget", thinking: &Thinking{Type: "enabled", BudgetTokens: 1024}, ceiling: "none", want: true},
		{name: "max allows the high budget only", thinking: &Thinking{Type: "enabled", BudgetTokens: 32000}, ceiling: "max", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ThinkingBudgetExceeds(tt.thinking, tt.ceiling))
		})
	}
}

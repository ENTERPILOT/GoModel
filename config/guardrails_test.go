package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A rule runs on every request unless it sets on_request.
func TestGuardrailRuleOnRequestDefaultsToFalse(t *testing.T) {
	result := loadYAML(t, []byte(`
guardrails:
  enabled: true
  rules:
    - name: "always"
      type: "system_prompt"
      system_prompt:
        content: "Be safe."
    - name: "asked"
      type: "system_prompt"
      on_request: true
      system_prompt:
        content: "Be brief."
`))

	rules := result.Config.Guardrails.Rules
	require.Len(t, rules, 2)
	assert.False(t, rules[0].OnRequest)
	assert.True(t, rules[1].OnRequest)
}

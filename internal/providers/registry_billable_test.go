package providers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestModelRegistry_ProviderBillable(t *testing.T) {
	registry := NewModelRegistry()

	assert.True(t, registry.ProviderBillable("openai"), "providers are billable unless configured otherwise")
	assert.True(t, registry.ProviderBillable(""))
	assert.True(t, (*ModelRegistry)(nil).ProviderBillable("openai"))

	registry.SetProviderBillable(" byok-openai ", false)
	assert.False(t, registry.ProviderBillable("byok-openai"))
	assert.True(t, registry.ProviderBillable("openai"))

	registry.SetProviderBillable("byok-openai", true)
	assert.True(t, registry.ProviderBillable("byok-openai"))

	// Unregistering forgets the flag, so a provider re-added under the same
	// name starts from the default.
	registry.SetProviderBillable("byok-openai", false)
	registry.UnregisterProvider("byok-openai")
	assert.True(t, registry.ProviderBillable("byok-openai"))
}

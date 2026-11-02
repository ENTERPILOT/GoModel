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

	// Unregistering keeps the flag: a replacement is unregistered and
	// re-registered in separate steps, and usage written in between must
	// still be marked. Removing a provider for good clears it explicitly.
	registry.SetProviderBillable("byok-openai", false)
	registry.UnregisterProvider("byok-openai")
	assert.False(t, registry.ProviderBillable("byok-openai"))
	registry.SetProviderBillable("byok-openai", true)
	assert.True(t, registry.ProviderBillable("byok-openai"))
}

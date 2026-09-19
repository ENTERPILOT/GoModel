package providers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// resetTrackingProvider adapts a plain provider into one implementing
// BreakerReset, counting resets.
type resetTrackingProvider struct {
	core.Provider
	resets int
}

func (p *resetTrackingProvider) ResetBreaker() {
	p.resets++
}

func TestBreakerResetter_ResetsRegisteredProvider(t *testing.T) {
	registry := NewModelRegistry()
	provider := &resetTrackingProvider{}
	registry.RegisterProviderWithNameAndType(provider, "openai-main", "openai")

	err := NewBreakerResetter(registry).ResetCircuitBreaker("openai-main")
	require.NoError(t, err)
	assert.Equal(t, 1, provider.resets)
}

func TestBreakerResetter_TrimsProviderName(t *testing.T) {
	registry := NewModelRegistry()
	provider := &resetTrackingProvider{}
	registry.RegisterProviderWithNameAndType(provider, "openai-main", "openai")

	err := NewBreakerResetter(registry).ResetCircuitBreaker("  openai-main  ")
	require.NoError(t, err)
	assert.Equal(t, 1, provider.resets)
}

func TestBreakerResetter_UnknownProvider(t *testing.T) {
	registry := NewModelRegistry()

	err := NewBreakerResetter(registry).ResetCircuitBreaker("missing")
	require.ErrorIs(t, err, ErrProviderNotFound)
}

func TestBreakerResetter_ProviderWithoutBreakerResetSupport(t *testing.T) {
	registry := NewModelRegistry()
	registry.RegisterProviderWithNameAndType(&registryMockProvider{name: "legacy"}, "legacy", "test")

	err := NewBreakerResetter(registry).ResetCircuitBreaker("legacy")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrProviderNotFound)
	assert.Contains(t, err.Error(), "does not support circuit breaker reset")
}

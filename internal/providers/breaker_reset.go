package providers

import (
	"errors"
	"fmt"
	"strings"
)

// ErrProviderNotFound reports a provider instance name nothing is registered
// under right now.
var ErrProviderNotFound = errors.New("provider not found")

// BreakerReset is implemented by providers whose underlying client can
// force-close its circuit breaker(s). Resetting is opt-in per adapter: the
// admin reset endpoint type-asserts this interface and reports the providers
// that lack it as not resettable rather than failing.
type BreakerReset interface {
	ResetBreaker()
}

// BreakerResetter force-closes a named provider's circuit breaker(s).
// It is the admin API's seam over the live registry; NewBreakerResetter
// builds the production implementation.
type BreakerResetter interface {
	ResetCircuitBreaker(providerName string) error
}

type registryBreakerResetter struct {
	registry *ModelRegistry
}

// NewBreakerResetter returns a BreakerResetter that resolves provider
// instance names against the live registry.
func NewBreakerResetter(registry *ModelRegistry) BreakerResetter {
	return registryBreakerResetter{registry: registry}
}

func (r registryBreakerResetter) ResetCircuitBreaker(providerName string) error {
	providerName = strings.TrimSpace(providerName)
	provider := r.registry.ProviderByName(providerName)
	if provider == nil {
		return fmt.Errorf("%w: %s", ErrProviderNotFound, providerName)
	}
	resetter, ok := provider.(BreakerReset)
	if !ok {
		return fmt.Errorf("provider %q does not support circuit breaker reset", providerName)
	}
	resetter.ResetBreaker()
	return nil
}

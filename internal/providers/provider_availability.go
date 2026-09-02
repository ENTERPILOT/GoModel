package providers

import (
	"context"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
)

// availabilityProbeTimeout bounds a single provider availability probe. Callers
// pass it explicitly so a path with a tighter latency budget than startup can
// choose its own without touching provider code.
const availabilityProbeTimeout = 5 * time.Second

// probeAvailability runs providerName's availability probe and records the
// outcome on the registry. Providers that do not implement
// core.AvailabilityChecker have nothing to probe and report available.
//
// The deadline lives at the call site rather than inside each provider's
// CheckAvailability so the budget can differ by path, and so a provider that
// omits a timeout of its own can never leave a probe unbounded.
func (r *ModelRegistry) probeAvailability(ctx context.Context, provider core.Provider, providerName string, timeout time.Duration) error {
	checker, ok := provider.(core.AvailabilityChecker)
	if !ok {
		return nil
	}

	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	err := checker.CheckAvailability(probeCtx)
	r.RecordAvailabilityCheck(providerName, err)
	return err
}

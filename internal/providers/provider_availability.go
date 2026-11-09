package providers

import (
	"context"
	"errors"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
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

	probeCtx, cancel := context.WithTimeout(llmclient.WithoutCircuitBreaker(ctx), timeout)
	defer cancel()

	err := checkAvailability(probeCtx, checker, providerName)
	if modelListingUnsupported(err) && r.hasConfiguredProviderModels(providerName) {
		// Probes that list models (Ollama, Bedrock Mantle) fail on servers
		// without a /models endpoint. The server answered, and its configured
		// models are the inventory, so it is reachable.
		err = nil
	}
	r.RecordAvailabilityCheck(providerName, err)
	return err
}

func (r *ModelRegistry) hasConfiguredProviderModels(providerName string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.configuredProviderModels[strings.TrimSpace(providerName)]) > 0
}

// checkAvailability runs the probe and turns a panic into an error: startup
// probes run on their own goroutines, where a panic would end the process.
func checkAvailability(ctx context.Context, checker core.AvailabilityChecker, providerName string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("provider availability probe panicked", "provider", providerName, "panic", r, "stack", string(debug.Stack()))
			err = errors.New("availability probe panicked")
		}
	}()
	return checker.CheckAvailability(ctx)
}

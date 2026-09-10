package plugins

import (
	"context"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/pluginapi"
)

// Health statuses of an instance.
const (
	// HealthOK is a healthy instance, and every instance of a plugin that
	// does not implement pluginapi.HealthChecker.
	HealthOK = "ok"
	// HealthDegraded is an instance whose last probe returned an error,
	// panicked, or ran past its deadline.
	HealthDegraded = "degraded"
)

// healthTimeout bounds one health probe; a variable so tests can shorten it.
var healthTimeout = 5 * time.Second

// Health is the outcome of an instance's last health probe.
type Health struct {
	Status string
	// Error is the probe's error text when Status is HealthDegraded.
	Error string
	// CheckedAt is when the probe ran; zero for a plugin that is not a
	// HealthChecker.
	CheckedAt time.Time
}

// Degraded reports whether the last probe failed.
func (h Health) Degraded() bool {
	return h.Status == HealthDegraded
}

// Checks reports whether the plugin implements pluginapi.HealthChecker.
func (i *Instance) Checks() bool {
	if i == nil {
		return false
	}
	_, ok := i.Plugin.(pluginapi.HealthChecker)
	return ok
}

// CheckHealth probes the instance when its plugin is a HealthChecker,
// records the outcome, and returns it. The probe runs with panic recovery
// under healthTimeout (and the instance timeout, when shorter); one that
// does not return in time counts as degraded. A plugin that is not a
// HealthChecker is reported ok without being called.
func (i *Instance) CheckHealth(ctx context.Context) Health {
	checker, ok := i.Plugin.(pluginapi.HealthChecker)
	if !ok {
		return Health{Status: HealthOK}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	_, err := Call(ctx, i, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, checker.Health(ctx)
	})
	h := Health{Status: HealthOK, CheckedAt: time.Now()}
	if err != nil {
		h.Status = HealthDegraded
		h.Error = strings.TrimSpace(err.Error())
	}
	i.health.Store(&h)
	return h
}

// Health returns the outcome of the last probe, or ok for a plugin that is
// not a HealthChecker or has not been probed yet.
func (i *Instance) Health() Health {
	if i == nil {
		return Health{Status: HealthOK}
	}
	if h := i.health.Load(); h != nil {
		return *h
	}
	return Health{Status: HealthOK}
}

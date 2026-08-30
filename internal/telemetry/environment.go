package telemetry

import (
	"os"
	"sync"
)

// The OpenTelemetry SDK configures itself from OTEL_* environment variables,
// so YAML settings reach it by being exported into the process environment.
// A variable the operator set in the environment keeps precedence over YAML.
// Variables exported here are remembered so that a reload can withdraw a
// YAML value that was removed rather than leaving the stale export in place.
var (
	exportedMu   sync.Mutex
	exportedKeys = map[string]struct{}{}
)

func exportEnvironment(vars map[string]string) {
	exportedMu.Lock()
	defer exportedMu.Unlock()

	for key := range exportedKeys {
		if _, still := vars[key]; !still {
			_ = os.Unsetenv(key)
			delete(exportedKeys, key)
		}
	}
	for key, value := range vars {
		if _, ours := exportedKeys[key]; !ours && os.Getenv(key) != "" {
			continue // set by the operator; environment wins over YAML
		}
		_ = os.Setenv(key, value)
		exportedKeys[key] = struct{}{}
	}
}

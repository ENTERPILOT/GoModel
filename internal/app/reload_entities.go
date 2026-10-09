package app

import (
	"errors"
	"fmt"
	"maps"
	"slices"
)

// keepServingEntities fails a replacement generation that would drop a
// dashboard-managed entity the serving generation runs, typically because
// the entity's stored secret references no longer resolve or resolve to an
// unusable value. The reload is rejected and the serving generation keeps
// running it, as for a config.yaml reference. An entity the serving
// generation skipped too does not block the reload.
//
// skipped maps each entity the replacement left out to why; serving reports
// whether the serving generation runs one.
func keepServingEntities(kind string, skipped map[string]error, serving func(name string) bool) error {
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(skipped)) {
		if serving(name) {
			errs = append(errs, fmt.Errorf("%q: %w", name, skipped[name]))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("serving %s would be dropped: %w", kind, errors.Join(errs...))
}

package app

import (
	"errors"
	"fmt"
	"maps"
	"slices"
)

// keepServingEntities fails a replacement generation that would drop a
// dashboard-managed entity the serving generation runs because the entity's
// stored secret references no longer resolve. The reload is rejected and the
// serving generation keeps running it, as for a config.yaml reference. An
// entity the serving generation skipped too does not block the reload.
//
// unresolved maps each entity the replacement skipped to why; serving
// reports whether the serving generation runs one.
func keepServingEntities(kind string, unresolved map[string]error, serving func(name string) bool) error {
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(unresolved)) {
		if serving(name) {
			errs = append(errs, unresolved[name])
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%s that are serving would be dropped because their secret references no longer resolve: %w", kind, errors.Join(errs...))
}

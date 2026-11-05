package app

import (
	"reflect"

	"github.com/enterpilot/gomodel/ext"
)

// sharedStoreAwareComponents lists the registered extension components that
// implement ext.SharedStoreAware, each pointer once even when it is
// registered in several roles.
func sharedStoreAwareComponents(registry *ext.Registry) []ext.SharedStoreAware {
	if registry == nil {
		return nil
	}
	var components []any
	components = appendComponents(components, registry.Authenticators())
	components = appendComponents(components, registry.Rewriters())
	components = appendComponents(components, registry.Settings())
	components = appendComponents(components, registry.HealthCheckers())
	components = append(components, registry.RouteSelector(), registry.ProxySelector())

	var aware []ext.SharedStoreAware
	seen := make(map[uintptr]bool)
	for _, component := range components {
		if nilInterface(component) {
			continue
		}
		target, ok := component.(ext.SharedStoreAware)
		if !ok {
			continue
		}
		if value := reflect.ValueOf(component); value.Kind() == reflect.Pointer {
			if seen[value.Pointer()] {
				continue
			}
			seen[value.Pointer()] = true
		}
		aware = append(aware, target)
	}
	return aware
}

func appendComponents[T any](components []any, registered []T) []any {
	for _, component := range registered {
		components = append(components, component)
	}
	return components
}

// bindSharedStore hands store to every shared-store-aware extension
// component. Like bindAuthenticationEventRecorders it runs only once the
// generation has built: the registry outlives generations, so a failed reload
// must not leave extensions holding a store it is about to close.
func bindSharedStore(registry *ext.Registry, store ext.SharedStore) {
	if store == nil || nilInterface(store) {
		return
	}
	for _, component := range sharedStoreAwareComponents(registry) {
		component.SetSharedStore(store)
	}
}

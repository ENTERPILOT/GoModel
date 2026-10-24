package app

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"

	"github.com/enterpilot/gomodel/config"
)

// entitySecrets re-resolves the secret references of one kind of
// dashboard-managed entity: provider credentials, MCP servers, or guardrails.
type entitySecrets interface {
	// RotateSecrets reinstalls the entities owning fields through their usual
	// install path. An entity that fails keeps its current values; the error
	// names it.
	RotateSecrets(ctx context.Context, fields []string) error
}

// entityRotation routes the changed fields under prefix to their entities.
type entityRotation struct {
	prefix   string
	entities entitySecrets
}

// watchEntities hands the fields under prefix ("provider_credentials.") to
// entities instead of the configuration. Call it before the watcher starts.
func (w *secretRotation) watchEntities(prefix string, entities entitySecrets) {
	if w == nil || entities == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.entities = append(w.entities, entityRotation{prefix: prefix, entities: entities})
}

// rotateEntities reinstalls the dashboard-managed entities whose referenced
// secrets changed and returns the rest of the recheck, the configuration's
// part. An entity re-records its references when it is reinstalled, so its
// fields are never committed here; one that failed is reported again on the
// next notification.
func (w *secretRotation) rotateEntities(ctx context.Context, recheck *config.SecretRecheck) *config.SecretRecheck {
	w.mu.Lock()
	entities := slices.Clone(w.entities)
	w.mu.Unlock()

	owned := func(field string) bool {
		return slices.ContainsFunc(entities, func(e entityRotation) bool { return strings.HasPrefix(field, e.prefix) })
	}
	for _, e := range entities {
		fields := recheck.Select(func(field string) bool { return strings.HasPrefix(field, e.prefix) }).Fields()
		if len(fields) == 0 {
			continue
		}
		slog.Info("referenced secrets of dashboard-managed entities changed; reinstalling them", "fields", fields)
		if err := e.entities.RotateSecrets(ctx, fields); err != nil {
			slog.Warn("rotated secrets could not be applied to every dashboard-managed entity; those keep their current values", "error", err)
		}
	}
	return recheck.Select(func(field string) bool { return !owned(field) })
}

// configFailed reports whether a recheck error names a field of the
// configuration rather than of a dashboard-managed entity. An error that
// names no field counts as the configuration's.
func (w *secretRotation) configFailed(err error) bool {
	w.mu.Lock()
	entities := slices.Clone(w.entities)
	w.mu.Unlock()

	var errs []error
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		errs = joined.Unwrap()
	} else {
		errs = []error{err}
	}
	for _, e := range errs {
		secretErr, ok := errors.AsType[*config.SecretError](e)
		if !ok || !slices.ContainsFunc(entities, func(r entityRotation) bool { return strings.HasPrefix(secretErr.Field, r.prefix) }) {
			return true
		}
	}
	return false
}

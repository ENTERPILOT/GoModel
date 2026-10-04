package app

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/providers"
)

// secretRecheckTimeout bounds one re-resolution of every recorded reference,
// so a backend that hangs cannot wedge later checks.
var secretRecheckTimeout = 30 * time.Second

// keySwap is a planned in-place swap of provider API keys.
type keySwap interface {
	Providers() []string
	Apply()
}

// secretRotation applies rotated secret references to the running generation
// (ADR-0014 §5). It waits for Secrets.NotifyChanged, re-resolves every recorded
// reference, and then:
//
//   - when only provider API keys changed, swaps them into the providers'
//     keyrings in place;
//   - when anything else changed, requests a generation reload, the same one
//     SIGHUP triggers;
//   - when a reference cannot be re-resolved, logs a warning and keeps every
//     current value. The backend that notified decides when values are stale;
//     a failed lookup is not evidence that the running credential is wrong.
//
// A change is recorded as applied only once its keys are swapped, so a
// reload that fails is retried on the next notification.
type secretRotation struct {
	secrets  *config.Secrets
	planKeys func(*config.SecretRecheck) keySwap
	// pinned names providers whose keys are also copied outside their
	// keyring (the semantic cache embedder); rotating those needs a reload.
	pinned []string
	reload func(reason string)

	mu sync.Mutex
	// entities re-resolve the references of dashboard-managed entities,
	// each owning the fields under its prefix.
	entities []entityRotation

	closed bool
	cancel context.CancelFunc
	done   chan struct{}
}

// providerKeyPlanner adapts providers.InitResult to secretRotation.
func providerKeyPlanner(result *providers.InitResult) func(*config.SecretRecheck) keySwap {
	return func(recheck *config.SecretRecheck) keySwap {
		if plan := result.PlanKeyRotation(recheck); plan != nil {
			return plan
		}
		return nil
	}
}

// start runs the watcher until Close or until ctx ends. It is called when the
// generation starts serving, never before: the process shares one notifier
// across generations, and a replacement still being built (or abandoned by a
// failed reload) must not take a notification meant for the one serving.
func (w *secretRotation) start(ctx context.Context) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.cancel != nil {
		return
	}
	ctx, w.cancel = context.WithCancel(ctx)
	w.done = make(chan struct{})
	go func() {
		defer close(w.done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-w.secrets.Changes():
				w.check(ctx)
			}
		}
	}()
}

// Close stops the watcher and waits for a check in progress to finish.
func (w *secretRotation) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	w.closed = true
	cancel, done := w.cancel, w.done
	w.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	return nil
}

func (w *secretRotation) check(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, secretRecheckTimeout)
	defer cancel()

	recheck, err := w.secrets.Recheck(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("secret references could not be re-resolved; keeping the current values", "error", err)
		}
		return
	}
	if len(recheck.Fields()) == 0 {
		slog.Debug("secret change notification: no referenced value changed")
		return
	}
	// Dashboard-managed entities reinstall themselves; only what is left
	// belongs to the configuration.
	recheck = w.rotateEntities(ctx, recheck)
	fields := recheck.Fields()
	if len(fields) == 0 {
		return
	}

	if plan := w.planKeys(recheck); plan != nil && !w.touchesPinned(plan.Providers()) {
		plan.Apply()
		recheck.Commit()
		if swapped := plan.Providers(); len(swapped) > 0 {
			slog.Info("rotated provider API keys in place", "providers", swapped, "fields", fields)
		} else {
			slog.Info("referenced provider API keys changed without changing any provider's effective keys", "fields", fields)
		}
		return
	}

	if w.reload == nil {
		slog.Warn("referenced secrets changed but this process cannot reload itself; reload or restart to apply them", "fields", fields)
		return
	}
	slog.Info("referenced secrets changed; reloading the configuration", "fields", fields)
	w.reload("secret references changed: " + strings.Join(fields, ", "))
}

func (w *secretRotation) touchesPinned(names []string) bool {
	return slices.ContainsFunc(names, func(name string) bool { return slices.Contains(w.pinned, name) })
}

// semanticEmbedderProvider names the provider the semantic response cache
// embeds with, or "" when that cache is off. The embedder copies the
// provider's keys when it is built, so their rotation needs a reload.
func semanticEmbedderProvider(cfg *config.Config) string {
	sem := cfg.Cache.Response.Semantic
	if sem == nil || !config.SemanticCacheActive(sem) {
		return ""
	}
	return strings.TrimSpace(sem.Embedder.Provider)
}

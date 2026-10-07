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

// secretRotationCloseTimeout bounds how long Close waits for a check in
// progress. A resolver that ignores its context must not hold up the rest of
// the shutdown; a check that outlives Close takes no action (see check).
var secretRotationCloseTimeout = 5 * time.Second

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

	mu     sync.Mutex
	closed bool
	cancel context.CancelFunc
	done   chan struct{}

	// handoff guards stranded and held. stranded is set while a re-check
	// abandoned after secretRecheckTimeout still waits on its resolver; held
	// records a notification that arrived meanwhile. One lock makes the
	// handoff exclusive: a notification either runs a check now or is held
	// and re-sent once, never both.
	handoff  sync.Mutex
	stranded bool
	held     bool
}

// recheckOutcome is the result of one re-resolution of every reference.
type recheckOutcome struct {
	recheck *config.SecretRecheck
	err     error
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
		// A notification held for a stranded re-check must not die with this
		// watcher: the generation serving next has to see it.
		defer w.forwardHeld()
		for {
			select {
			case <-ctx.Done():
				return
			case <-w.secrets.Changes():
				if ctx.Err() != nil {
					// Both were ready and select took the notification:
					// hand it back to whichever generation serves next.
					w.secrets.NotifyChanged()
					return
				}
				w.check(ctx)
			}
		}
	}()
}

// Close stops the watcher and waits, up to secretRotationCloseTimeout, for a
// check in progress to finish. A check still running after that is left
// behind: it acts on nothing once it returns.
func (w *secretRotation) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	w.closed = true
	cancel, done := w.cancel, w.done
	w.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
	case <-time.After(secretRotationCloseTimeout):
		slog.Warn("a secret resolver did not return after cancellation; leaving its check behind",
			"waited", secretRotationCloseTimeout)
	}
	return nil
}

// check re-resolves, then swaps keys or requests a reload. generation is the
// serving generation's context: once it ends, a check that was still
// resolving drops its result and hands the notification on, so the next
// generation re-checks with its own resolvers.
//
// The re-resolution runs on its own goroutine, and check stops waiting for it
// after secretRecheckTimeout: a resolver that ignores its context must not
// wedge the watcher. Its late result is discarded. Until it returns, further
// notifications are held instead of starting another re-check the same
// resolver would block, and one is re-sent once it does.
func (w *secretRotation) check(generation context.Context) {
	if w.holdIfStranded() {
		slog.Warn("a secret re-check is still waiting on a resolver that did not return; this change is checked once it does")
		return
	}
	ctx, cancel := context.WithTimeout(generation, secretRecheckTimeout)
	defer cancel()

	result := make(chan recheckOutcome, 1) // buffered: an abandoned re-check never blocks on send
	go func() {
		recheck, err := w.secrets.Recheck(ctx)
		result <- recheckOutcome{recheck, err}
	}()
	var out recheckOutcome
	select {
	case out = <-result:
	case <-ctx.Done():
		if generation.Err() == nil {
			slog.Warn("secret references were not re-resolved in time; keeping the current values",
				"timeout", secretRecheckTimeout)
			w.strand(result)
			return
		}
	}
	if generation.Err() != nil {
		w.secrets.NotifyChanged()
		return
	}
	recheck, err := out.recheck, out.err
	if err != nil {
		slog.Warn("secret references could not be re-resolved; keeping the current values", "error", err)
		return
	}
	fields := recheck.Fields()
	if len(fields) == 0 {
		slog.Debug("secret change notification: no referenced value changed")
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

// strand records a re-check that outlived secretRecheckTimeout. Once it
// returns, its result is dropped and a notification held meanwhile is sent
// again.
func (w *secretRotation) strand(result <-chan recheckOutcome) {
	w.handoff.Lock()
	w.stranded = true
	w.handoff.Unlock()
	go func() {
		<-result
		w.handoff.Lock()
		w.stranded = false
		w.handoff.Unlock()
		w.forwardHeld()
	}()
}

// holdIfStranded reports whether a stranded re-check is still running and, if
// so, holds the current notification for it to re-send.
func (w *secretRotation) holdIfStranded() bool {
	w.handoff.Lock()
	defer w.handoff.Unlock()
	if w.stranded {
		w.held = true
	}
	return w.stranded
}

// forwardHeld re-sends a held notification, at most once.
func (w *secretRotation) forwardHeld() {
	w.handoff.Lock()
	resend := w.held
	w.held = false
	w.handoff.Unlock()
	if resend {
		w.secrets.NotifyChanged()
	}
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

package ratelimit

import (
	"context"
	"log/slog"
	"math"
	"time"
)

const persistTimeout = 5 * time.Second

type persistState int

const (
	persistIdle persistState = iota
	persistStarting
	persistActive
	persistClosed
)

// WithFlushInterval sets how often an active generation writes its changed
// counters and reads the other writers'. Zero disables the periodic loop;
// Start still loads and Close of an active generation still writes once, but
// nothing counted elsewhere after Start is seen.
func WithFlushInterval(interval time.Duration) ServiceOption {
	return func(service *Service) {
		if interval < 0 {
			interval = 0
		}
		service.flushInterval = interval
	}
}

// flushIntervalFromSeconds converts the configured seconds, clamping an
// absurd value rather than letting it overflow into a negative duration.
func flushIntervalFromSeconds(seconds int) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(min(int64(seconds), int64(math.MaxInt64/time.Second))) * time.Second
}

func persistContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent != nil {
		if _, ok := parent.Deadline(); ok {
			return context.WithCancel(parent)
		}
		return context.WithTimeout(parent, persistTimeout)
	}
	return context.WithTimeout(context.Background(), persistTimeout)
}

// Start loads the counters every writer saved, earlier runs of this one
// included, and from then on keeps writing this generation's and reading the
// others'. It is separate from NewService because a reload builds the next
// generation while the current one still serves: only the generation that
// gets to serve may touch the snapshot, or a replacement that is built and
// then discarded would publish its counters too. Start is idempotent, and a
// failed load leaves the generation idle rather than persisting from a blank
// slate.
//
// The loaded rows become the limiter's remote counters, not its own: this
// generation's rows only ever hold what it counted itself (see
// WindowSnapshot), so a restart counts its predecessor's requests once,
// through their rows, rather than again through its own.
func (s *Service) Start(ctx context.Context) {
	if s == nil || s.store == nil {
		return
	}

	s.lifeMu.Lock()
	if s.persistState != persistIdle {
		s.lifeMu.Unlock()
		return
	}
	s.persistState = persistStarting
	s.lifeMu.Unlock()

	loadedAt := s.now()
	loadCtx, cancel := persistContext(ctx)
	snapshots, err := s.store.LoadCounters(loadCtx, 0)
	cancel()

	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	if s.persistState != persistStarting {
		return
	}
	if err != nil {
		s.persistState = persistIdle
		slog.Warn("rate limit counters: load failed; not persisting this generation", "error", err)
		return
	}
	s.installPeers(snapshots, loadedAt, true)
	s.startFlushLoop()
	s.persistState = persistActive
	if len(snapshots) > 0 {
		slog.Info("rate limit counters restored", "windows", len(snapshots))
	}
}

func (s *Service) startFlushLoop() {
	if s.flushInterval <= 0 {
		return
	}
	s.flushStop = make(chan struct{})
	s.flushDone = make(chan struct{})
	interval := s.flushInterval
	stop := s.flushStop
	done := s.flushDone
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.flush(context.Background())
			case <-stop:
				return
			}
		}
	}()
}

// flush is one tick of the flush loop: it saves this writer's changes, then
// reads the other writers'. The read comes second so that, within one tick,
// peers see this writer's counts no later than it sees theirs.
func (s *Service) flush(ctx context.Context) {
	if s == nil || s.store == nil {
		return
	}
	s.save(ctx)
	if err := s.loadPeers(ctx); err != nil {
		slog.Warn("rate limit counters: load failed", "error", err)
	}
}

// save writes the rows of the counters that changed since the last save.
func (s *Service) save(ctx context.Context) {
	if s == nil || s.store == nil {
		return
	}
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	snapshots := s.limiter.snapshot(s.Rules())
	if len(snapshots) == 0 {
		return
	}
	for i := range snapshots {
		snapshots[i].Writer = s.writer
	}
	saveCtx, cancel := persistContext(ctx)
	defer cancel()
	if err := s.store.SaveCounters(saveCtx, snapshots); err != nil {
		s.limiter.unsaved(snapshots)
		slog.Warn("rate limit counters: flush failed", "error", err)
	}
}

func (s *Service) persistReset(ctx context.Context, scope RuleScope, subject string, periodSeconds int64) error {
	if s == nil || s.store == nil {
		return nil
	}
	// Take the lock before starting the clock: a reset that queued behind a
	// slow flush would otherwise spend its whole budget waiting.
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	writeCtx, cancel := persistContext(ctx)
	defer cancel()
	return s.store.ResetCounter(writeCtx, scope, subject, periodSeconds)
}

func (s *Service) persistResetAll(ctx context.Context) error {
	if s == nil || s.store == nil {
		return nil
	}
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	writeCtx, cancel := persistContext(ctx)
	defer cancel()
	return s.store.ResetAllCounters(writeCtx)
}

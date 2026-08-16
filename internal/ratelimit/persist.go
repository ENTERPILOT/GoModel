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

// WithFlushInterval sets how often an active generation writes window
// snapshots. Zero disables the periodic loop; Start still loads and Close
// of an active generation still writes once.
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

// Start restores persisted windows and, from then on, keeps writing them.
// It is separate from NewService because a reload builds the next generation
// while the current one still serves: only the generation that gets to serve
// may touch the snapshot, or a replacement that is built and then discarded
// would flush its empty windows over the live ones. Start is idempotent, and
// a failed load leaves the generation idle rather than persisting from a
// blank slate.
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

	loadCtx, cancel := persistContext(ctx)
	snapshots, err := s.store.LoadCounters(loadCtx)
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
	s.limiter.restore(snapshots, s.Rules(), time.Now().UTC())
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
				flushCtx, cancel := persistContext(context.Background())
				s.flush(flushCtx)
				cancel()
			case <-stop:
				return
			}
		}
	}()
}

func (s *Service) flush(ctx context.Context) {
	if s == nil || s.store == nil {
		return
	}
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	if err := s.store.SaveCounters(ctx, s.limiter.snapshot(s.Rules())); err != nil {
		slog.Warn("rate limit counters: flush failed", "error", err)
	}
}

func (s *Service) persistDelete(ctx context.Context, scope RuleScope, subject string, periodSeconds int64) error {
	if s == nil || s.store == nil {
		return nil
	}
	// Take the lock before starting the clock: a delete that queued behind a
	// slow flush would otherwise spend its whole budget waiting.
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	writeCtx, cancel := persistContext(ctx)
	defer cancel()
	return s.store.DeleteCounter(writeCtx, scope, subject, periodSeconds)
}

func (s *Service) persistDeleteAll(ctx context.Context) error {
	if s == nil || s.store == nil {
		return nil
	}
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	writeCtx, cancel := persistContext(ctx)
	defer cancel()
	return s.store.DeleteAllCounters(writeCtx)
}

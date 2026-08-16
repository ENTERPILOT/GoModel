package ratelimit

import (
	"context"
	"log/slog"
	"time"
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

func (s *Service) Start(ctx context.Context) {
	if s == nil || s.store == nil {
		return
	}
	s.loadCounters(ctx)
	s.startFlushLoop()
	s.active.Store(true)
}

func (s *Service) loadCounters(ctx context.Context) {
	snapshots, err := s.store.LoadCounters(ctx)
	if err != nil {
		slog.Warn("rate limit counters: load failed; starting empty", "error", err)
		return
	}
	s.limiter.restore(snapshots, s.Rules(), time.Now().UTC())
}

func (s *Service) startFlushLoop() {
	if s.flushInterval <= 0 {
		return
	}
	s.flushStop = make(chan struct{})
	s.flushDone = make(chan struct{})
	go func() {
		defer close(s.flushDone)
		ticker := time.NewTicker(s.flushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.flush(context.Background())
			case <-s.flushStop:
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

func (s *Service) persistDelete(scope RuleScope, subject string, periodSeconds int64) {
	if s == nil || s.store == nil {
		return
	}
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	if err := s.store.DeleteCounter(context.Background(), scope, subject, periodSeconds); err != nil {
		slog.Error("rate limit counters: delete failed", "scope", scope, "subject", subject, "period_seconds", periodSeconds, "error", err)
	}
}

func (s *Service) persistDeleteAll() {
	if s == nil || s.store == nil {
		return
	}
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	if err := s.store.DeleteAllCounters(context.Background()); err != nil {
		slog.Error("rate limit counters: delete-all failed", "error", err)
	}
}

func (s *Service) stopFlushAndSave() {
	s.flushOnce.Do(func() {
		if s.flushStop != nil {
			close(s.flushStop)
			<-s.flushDone
		}
		if s.active.Load() {
			s.flush(context.Background())
		}
	})
}

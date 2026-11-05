package runtimesettings

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

const syncInterval = 2 * time.Second

// syncTimeout bounds reading the stored values on one tick, so a database
// that stops answering fails the tick, which the next one retries, instead of
// stalling reconciliation and holding a pooled connection indefinitely.
const syncTimeout = 5 * time.Second

func (s *Service) startSync() {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(syncInterval)
		defer ticker.Stop()
		lastError := ""
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				err := s.sync(ctx)
				if err == nil {
					lastError = ""
				} else if !errors.Is(err, context.Canceled) && err.Error() != lastError {
					lastError = err.Error()
					slog.Warn("failed to synchronize runtime settings", "error", err)
				}
			}
		}
	}()
}

// sync reconciles extension state with the shared database. It intentionally
// runs outside request handling and applies only changed values. It reads
// every value in one query, bounded by syncTimeout, without holding s.mu, so a
// slow database never blocks Update or List.
func (s *Service) sync(ctx context.Context) error {
	s.mu.Lock()
	updates := s.updates
	s.mu.Unlock()
	readCtx, cancel := context.WithTimeout(ctx, syncTimeout)
	values, err := s.store.GetAll(readCtx)
	cancel()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updates != updates {
		// An Update ran during the read, so the values may predate it; the
		// next tick reads them again.
		return nil
	}
	var errs []error
	for _, key := range s.order {
		setting := s.settings[key]
		descriptor := setting.Descriptor()
		if descriptor.Locked {
			continue
		}
		value, found := values[key]
		if !found || value == descriptor.Value {
			delete(s.rejected, key)
			continue
		}
		if !valueAllowed(descriptor, value) {
			if s.rejected[key] != value {
				slog.Warn("ignoring invalid synchronized runtime setting", "key", key, "value", value)
				s.rejected[key] = value
			}
			continue
		}
		delete(s.rejected, key)
		if err := setting.Apply(value); err != nil {
			errs = append(errs, fmt.Errorf("apply synchronized runtime setting %q: %w", key, err))
		}
	}
	return errors.Join(errs...)
}

package runtimesettings

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

const syncInterval = 2 * time.Second

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
// runs outside request handling and applies only changed values.
func (s *Service) sync(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	for _, key := range s.order {
		setting := s.settings[key]
		descriptor := setting.Descriptor()
		if descriptor.Locked {
			continue
		}
		value, found, err := s.store.Get(ctx, key)
		if err != nil {
			errs = append(errs, fmt.Errorf("get synchronized runtime setting %q: %w", key, err))
			continue
		}
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

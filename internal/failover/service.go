package failover

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gomodel/config"
)

// Service merges dashboard-managed rules with read-only config/env rules.
type Service struct {
	store      Store
	configRows []Rule
	current    atomic.Value // []Rule
	refreshMu  sync.Mutex
}

func NewService(store Store, cfg config.FallbackConfig) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	service := &Service{store: store, configRows: ConfigRules(cfg)}
	service.current.Store([]Rule{})
	return service, nil
}

func ConfigRules(cfg config.FallbackConfig) []Rule {
	rows := make([]Rule, 0, len(cfg.Manual))
	now := time.Now().UTC()
	for source, targets := range cfg.Manual {
		source = strings.TrimSpace(source)
		if source == "" {
			continue
		}
		cleanTargets := normalizeTargets(targets)
		rows = append(rows, Rule{
			Source:        source,
			Targets:       cleanTargets,
			Enabled:       !cfg.Disabled[source],
			ManagedSource: ManagedSourceConfig,
			CreatedAt:     now,
			UpdatedAt:     now,
		})
	}
	for source := range cfg.Disabled {
		source = strings.TrimSpace(source)
		if source == "" {
			continue
		}
		if hasRule(rows, source) {
			continue
		}
		rows = append(rows, Rule{
			Source:        source,
			Enabled:       false,
			ManagedSource: ManagedSourceConfig,
			CreatedAt:     now,
			UpdatedAt:     now,
		})
	}
	return rows
}

func hasRule(rows []Rule, source string) bool {
	for _, row := range rows {
		if row.Source == source {
			return true
		}
	}
	return false
}

func (s *Service) Refresh(ctx context.Context) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	rows, err := s.store.List(ctx)
	if err != nil {
		return fmt.Errorf("list failover rules: %w", err)
	}
	s.current.Store(s.mergeConfig(rows))
	return nil
}

func (s *Service) mergeConfig(stored []Rule) []Rule {
	managed := make(map[string]struct{}, len(s.configRows))
	for _, row := range s.configRows {
		managed[row.Source] = struct{}{}
	}
	merged := make([]Rule, 0, len(stored)+len(s.configRows))
	for _, row := range stored {
		if _, ok := managed[strings.TrimSpace(row.Source)]; ok {
			continue
		}
		row.ManagedSource = ManagedSourceDashboard
		merged = append(merged, row.clone())
	}
	for _, row := range s.configRows {
		merged = append(merged, row.clone())
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Source < merged[j].Source })
	return merged
}

func (s *Service) Rules() map[string][]string {
	rows := s.List()
	result := make(map[string][]string, len(rows))
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		targets := normalizeTargets(row.Targets)
		if len(targets) == 0 {
			continue
		}
		result[row.Source] = targets
	}
	return result
}

func (s *Service) Disabled() map[string]bool {
	rows := s.List()
	result := make(map[string]bool)
	for _, row := range rows {
		if !row.Enabled {
			result[row.Source] = true
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func (s *Service) List() []Rule {
	if s == nil {
		return nil
	}
	rows := s.current.Load().([]Rule)
	out := make([]Rule, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.clone())
	}
	return out
}

func (s *Service) ListViews() []View {
	rows := s.List()
	views := make([]View, 0, len(rows))
	for _, row := range rows {
		views = append(views, row.view())
	}
	return views
}

func (s *Service) Get(source string) (*Rule, bool) {
	source = strings.TrimSpace(source)
	for _, row := range s.List() {
		if row.Source == source {
			return &row, true
		}
	}
	return nil, false
}

func (s *Service) Upsert(ctx context.Context, rule Rule) error {
	if s == nil {
		return fmt.Errorf("failover service is required")
	}
	normalized, err := normalizeRule(rule)
	if err != nil {
		return err
	}
	if s.isManagedSource(normalized.Source) {
		return ErrManaged
	}
	normalized.ManagedSource = ManagedSourceDashboard
	if existing, err := s.store.Get(ctx, normalized.Source); err == nil && existing != nil {
		normalized.CreatedAt = existing.CreatedAt
	}
	if err := s.store.Upsert(ctx, normalized); err != nil {
		return err
	}
	return s.Refresh(ctx)
}

func (s *Service) Delete(ctx context.Context, source string) error {
	source = strings.TrimSpace(source)
	if source == "" {
		return fmt.Errorf("source is required")
	}
	if s.isManagedSource(source) {
		return ErrManaged
	}
	if err := s.store.Delete(ctx, source); err != nil {
		return err
	}
	return s.Refresh(ctx)
}

func (s *Service) ResetDashboardRules(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("failover service is required")
	}
	if err := s.store.DeleteAll(ctx); err != nil {
		return err
	}
	return s.Refresh(ctx)
}

func (s *Service) isManagedSource(source string) bool {
	source = strings.TrimSpace(source)
	for _, row := range s.configRows {
		if row.Source == source {
			return true
		}
	}
	return false
}

func (s *Service) StartBackgroundRefresh(interval time.Duration) func() {
	if interval <= 0 {
		interval = time.Hour
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refreshCtx, refreshCancel := context.WithTimeout(ctx, 30*time.Second)
				if err := s.Refresh(refreshCtx); err != nil {
					slog.Error("failed to refresh failover rules", "error", err)
				}
				refreshCancel()
			}
		}
	}()
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}

func normalizeRule(rule Rule) (Rule, error) {
	rule.Source = strings.TrimSpace(rule.Source)
	if rule.Source == "" {
		return Rule{}, fmt.Errorf("source is required")
	}
	rule.Targets = normalizeTargets(rule.Targets)
	if rule.Enabled && len(rule.Targets) == 0 {
		return Rule{}, fmt.Errorf("targets must contain at least one model")
	}
	rule.Description = strings.TrimSpace(rule.Description)
	return rule, nil
}

func normalizeTargets(targets []string) []string {
	seen := make(map[string]struct{}, len(targets))
	out := make([]string, 0, len(targets))
	for _, target := range targets {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		if _, ok := seen[target]; ok {
			continue
		}
		seen[target] = struct{}{}
		out = append(out, target)
	}
	return out
}

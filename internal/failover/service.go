package failover

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gomodel/config"
)

// Service merges dashboard-managed mappings with read-only config/env mappings.
type Service struct {
	store      Store
	configRows []Rule
	current    atomic.Value // *ruleSnapshot
	refreshMu  sync.Mutex
}

// ruleSnapshot is the immutable, atomically-published view of the merged rules.
// It caches the derived Rules/Disabled lookup maps so the per-request resolver
// hot path reads them without re-cloning rows or rebuilding maps on every call.
// The maps and their slices must be treated as read-only by callers.
type ruleSnapshot struct {
	rows     []Rule
	rules    map[string][]string
	disabled map[string]bool
}

func newRuleSnapshot(rows []Rule) *ruleSnapshot {
	rules := make(map[string][]string)
	disabled := make(map[string]bool)
	for _, row := range rows {
		if !row.Enabled {
			disabled[row.Source] = true
			continue
		}
		targets := normalizeTargets(row.Targets)
		if len(targets) == 0 {
			continue
		}
		rules[row.Source] = targets
	}
	return &ruleSnapshot{rows: rows, rules: rules, disabled: disabled}
}

func NewService(store Store, cfg config.FallbackConfig) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	service := &Service{store: store, configRows: ConfigRules(cfg)}
	service.current.Store(newRuleSnapshot(nil))
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
		return fmt.Errorf("list failover mappings: %w", err)
	}
	s.current.Store(newRuleSnapshot(s.mergeConfig(rows)))
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

// Rules returns the enabled source -> targets map. The returned map is the
// cached snapshot and must not be mutated by callers.
func (s *Service) Rules() map[string][]string {
	snap := s.loadSnapshot()
	if snap == nil {
		return nil
	}
	return snap.rules
}

// Disabled returns the set of disabled sources, or nil when none. The returned
// map is the cached snapshot and must not be mutated by callers.
func (s *Service) Disabled() map[string]bool {
	snap := s.loadSnapshot()
	if snap == nil || len(snap.disabled) == 0 {
		return nil
	}
	return snap.disabled
}

func (s *Service) loadSnapshot() *ruleSnapshot {
	if s == nil {
		return nil
	}
	snap, _ := s.current.Load().(*ruleSnapshot)
	return snap
}

func (s *Service) List() []Rule {
	snap := s.loadSnapshot()
	if snap == nil {
		return nil
	}
	out := make([]Rule, 0, len(snap.rows))
	for _, row := range snap.rows {
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
	existing, err := s.store.Get(ctx, normalized.Source)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("read existing failover rule: %w", err)
	}
	if existing != nil {
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
		return fmt.Errorf("primary model is required")
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
					slog.Error("failed to refresh failover mappings", "error", err)
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
		return Rule{}, fmt.Errorf("primary model is required")
	}
	rule.Targets = normalizeTargets(rule.Targets)
	if rule.Enabled && len(rule.Targets) == 0 {
		return Rule{}, fmt.Errorf("targets must contain at least one model")
	}
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

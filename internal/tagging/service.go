package tagging

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

// snapshot is the immutable, atomically swapped view served on the hot path.
type snapshot struct {
	rules []Rule
	strip map[string]struct{}
}

// Service merges declarative (config/env) tagging rules over operator rules
// persisted in the store and serves label extraction on the request hot path.
type Service struct {
	store Store

	// configRules are supplied declaratively (config.yaml / TAGGING_HEADER_*).
	// They override store rows with the same header and are read-only.
	configRules []Rule

	current   atomic.Value // snapshot
	refreshMu sync.Mutex
}

// NewService creates a tagging service. configRules must already be normalized
// by config.Load; store may be nil, in which case only config rules apply and
// dashboard edits are unavailable.
func NewService(configRules []Rule, store Store) *Service {
	managed := make([]Rule, len(configRules))
	for i, rule := range configRules {
		rule.Managed = true
		managed[i] = rule
	}
	service := &Service{store: store, configRules: managed}
	service.current.Store(buildSnapshot(managed))
	return service
}

func buildSnapshot(rules []Rule) snapshot {
	return snapshot{rules: rules, strip: StripHeaderSet(rules)}
}

func (s *Service) snapshot() snapshot {
	if s == nil {
		return snapshot{}
	}
	return s.current.Load().(snapshot)
}

// Refresh reloads operator rules from the store and swaps the merged snapshot.
func (s *Service) Refresh(ctx context.Context) error {
	if s == nil || s.store == nil {
		return nil
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	stored, err := s.store.GetRules(ctx)
	if err != nil {
		return fmt.Errorf("load tagging rules: %w", err)
	}
	if err := NormalizeRules(stored); err != nil {
		return fmt.Errorf("stored tagging rules: %w", err)
	}
	s.current.Store(buildSnapshot(s.mergeConfigRules(stored)))
	return nil
}

// mergeConfigRules overlays the config-managed rules onto the store rows.
// Store rows shadowed by a managed rule of the same header are dropped, so
// config stays the source of truth for the headers it declares.
func (s *Service) mergeConfigRules(stored []Rule) []Rule {
	merged := make([]Rule, 0, len(s.configRules)+len(stored))
	merged = append(merged, s.configRules...)
	for _, rule := range stored {
		if s.isManagedHeader(rule.Header) {
			continue
		}
		rule.Managed = false
		merged = append(merged, rule)
	}
	return merged
}

// isManagedHeader reports whether header is owned by a declarative config rule.
func (s *Service) isManagedHeader(header string) bool {
	for _, rule := range s.configRules {
		if strings.EqualFold(rule.Header, header) {
			return true
		}
	}
	return false
}

// Rules returns the effective rules: managed config rules first, then
// operator rules from the store.
func (s *Service) Rules() []Rule {
	current := s.snapshot()
	rules := make([]Rule, len(current.rules))
	copy(rules, current.rules)
	return rules
}

// SaveRules validates and persists the operator-managed rule set (replacing
// the previous set), refreshes the snapshot, and returns the merged view.
// Rules whose header is declared in config/env are rejected as read-only.
func (s *Service) SaveRules(ctx context.Context, rules []Rule) ([]Rule, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("tagging rules storage is not available")
	}
	if err := NormalizeRules(rules); err != nil {
		return nil, err
	}
	for _, rule := range rules {
		if s.isManagedHeader(rule.Header) {
			return nil, newValidationError("header %q is managed by config/env and is read-only", rule.Header)
		}
	}
	for i := range rules {
		rules[i].Managed = false
	}
	if err := s.store.SaveRules(ctx, rules); err != nil {
		return nil, fmt.Errorf("save tagging rules: %w", err)
	}
	if err := s.Refresh(ctx); err != nil {
		return nil, err
	}
	return s.Rules(), nil
}

// Editable reports whether operator rules can be persisted.
func (s *Service) Editable() bool {
	return s != nil && s.store != nil
}

// ExtractLabels returns the request labels for the given inbound headers.
func (s *Service) ExtractLabels(headers http.Header) []string {
	return ExtractLabels(s.snapshot().rules, headers)
}

// StripHeaders returns the canonical header names that must not be forwarded
// to upstream providers. Callers must treat the returned map as read-only.
func (s *Service) StripHeaders() map[string]struct{} {
	return s.snapshot().strip
}

// HasRules reports whether any tagging rule is currently active.
func (s *Service) HasRules() bool {
	return len(s.snapshot().rules) > 0
}

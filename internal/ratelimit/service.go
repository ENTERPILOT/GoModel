package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrUnavailable indicates a rate limit service was used without an initialized store.
var ErrUnavailable = errors.New("rate limit service is unavailable")

// ErrQuotaTemplatesUnavailable indicates that a per-child template was found
// while the deployment does not have the quota-templates capability.
var ErrQuotaTemplatesUnavailable = errors.New("per-child quota templates are not enabled")

// ServiceOption configures a rate limit service.
type ServiceOption func(*Service)

// WithQuotaTemplates controls whether per-child user-path rules are accepted.
func WithQuotaTemplates(enabled bool) ServiceOption {
	return func(service *Service) {
		service.quotaTemplates = enabled
	}
}

type Service struct {
	store   Store
	limiter *limiter

	mu    sync.RWMutex
	rules []Rule

	quotaTemplates bool

	flushInterval time.Duration
	persistMu     sync.Mutex
	lifeMu        sync.Mutex
	persistState  persistState
	flushStop     chan struct{}
	flushDone     chan struct{}
}

func NewService(ctx context.Context, store Store, options ...ServiceOption) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("rate limit store is required")
	}
	service := &Service{
		store:          store,
		limiter:        newLimiter(),
		quotaTemplates: true,
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	if err := service.Refresh(ctx); err != nil {
		return nil, err
	}
	return service, nil
}

// Close stops the flush loop, writes a final snapshot if this generation
// was started, and stops the in-memory expiry cleanup worker.
func (s *Service) Close() {
	if s == nil {
		return
	}
	s.lifeMu.Lock()
	if s.persistState == persistClosed {
		s.lifeMu.Unlock()
		return
	}
	wasActive := s.persistState == persistActive
	s.persistState = persistClosed
	stop := s.flushStop
	done := s.flushDone
	s.flushStop = nil
	s.flushDone = nil
	s.lifeMu.Unlock()

	if stop != nil {
		close(stop)
		<-done
	}
	if wasActive {
		flushCtx, cancel := persistContext(context.Background())
		s.flush(flushCtx)
		cancel()
	}
	if s.limiter != nil {
		s.limiter.close()
	}
}

func (s *Service) Refresh(ctx context.Context) error {
	if s == nil || s.store == nil {
		return ErrUnavailable
	}
	rules, err := s.store.ListRules(ctx)
	if err != nil {
		return err
	}
	if err := s.validateQuotaTemplates(rules); err != nil {
		return err
	}
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].Scope != rules[j].Scope {
			return rules[i].Scope < rules[j].Scope
		}
		if rules[i].Subject != rules[j].Subject {
			return rules[i].Subject < rules[j].Subject
		}
		return rules[i].PeriodSeconds < rules[j].PeriodSeconds
	})
	s.mu.Lock()
	previous := s.rules
	s.rules = rules
	s.mu.Unlock()

	// Removed definitions and shared/per-child mode changes must not leave
	// counters that can reappear if the same key is added again later.
	next := make(map[ruleKey]Rule, len(rules))
	for _, rule := range rules {
		next[keyForRule(rule)] = rule
	}
	for _, old := range previous {
		newRule, exists := next[keyForRule(old)]
		if !exists || old.PerChild != newRule.PerChild {
			s.limiter.reset(keyForRule(old))
		}
	}
	return nil
}

func (s *Service) Rules() []Rule {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Rule(nil), s.rules...)
}

func (s *Service) UpsertRules(ctx context.Context, rules []Rule) error {
	if s == nil || s.store == nil {
		return ErrUnavailable
	}
	if err := s.validateQuotaTemplates(rules); err != nil {
		return err
	}
	if err := s.store.UpsertRules(ctx, rules); err != nil {
		return err
	}
	return s.Refresh(ctx)
}

func (s *Service) DeleteRule(ctx context.Context, scope RuleScope, subject string, periodSeconds int64) error {
	if s == nil || s.store == nil {
		return ErrUnavailable
	}
	scope, subject, err := normalizeRuleKey(scope, subject, periodSeconds)
	if err != nil {
		return err
	}
	if err := s.store.DeleteRule(ctx, scope, subject, periodSeconds); err != nil {
		return err
	}
	s.limiter.reset(ruleKey{scope: scope, subject: subject, periodSeconds: periodSeconds})
	if err := s.persistDelete(ctx, scope, subject, periodSeconds); err != nil {
		return err
	}
	return s.Refresh(ctx)
}

func (s *Service) ReplaceConfigRules(ctx context.Context, rules []Rule) error {
	if s == nil || s.store == nil {
		return ErrUnavailable
	}
	if err := s.validateQuotaTemplates(rules); err != nil {
		return err
	}
	if err := s.store.ReplaceConfigRules(ctx, rules); err != nil {
		return err
	}
	return s.Refresh(ctx)
}

func (s *Service) validateQuotaTemplates(rules []Rule) error {
	if s == nil || s.quotaTemplates {
		return nil
	}
	for _, rule := range rules {
		if rule.PerChild {
			return fmt.Errorf("%w: rate limit for user path %q", ErrQuotaTemplatesUnavailable, rule.Subject)
		}
	}
	return nil
}

// HasTokenRules reports whether any rule limits tokens. Used at startup to
// warn when token limits cannot be enforced because usage tracking is off.
func (s *Service) HasTokenRules() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, rule := range s.rules {
		if rule.MaxTokens != nil {
			return true
		}
	}
	return false
}

// Reservation represents one admitted request. Release must be called when
// the request finishes so concurrency slots return; it is idempotent.
type Reservation struct {
	limiter *limiter
	held    []ruleKey
	headers HeaderSnapshot
	once    sync.Once
}

func (r *Reservation) Release() {
	if r == nil || r.limiter == nil {
		return
	}
	r.once.Do(func() { r.limiter.release(r.held) })
}

// Headers returns the x-ratelimit-* snapshot captured at admission.
func (r *Reservation) Headers() HeaderSnapshot {
	if r == nil {
		return HeaderSnapshot{}
	}
	return r.headers
}

// Acquire admits or rejects a request for the given subjects. On rejection
// the returned error unwraps to *ExceededError. The returned reservation is
// always non-nil on success.
func (s *Service) Acquire(subjects Subjects, now time.Time) (*Reservation, error) {
	if s == nil {
		return &Reservation{}, nil
	}
	subjects, err := normalizeSubjects(subjects)
	if err != nil {
		return nil, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()

	matching := s.matchingRules(subjects)
	if len(matching) == 0 {
		return &Reservation{}, nil
	}
	headers, held, exceeded := s.limiter.admit(matching, now)
	if exceeded != nil {
		return nil, exceeded
	}
	return &Reservation{limiter: s.limiter, held: held, headers: headers}, nil
}

// RouteAvailable reports whether the provider/model route currently has
// rate-limit capacity. Load balancing and failover use it to skip saturated
// targets; user-path rules are intentionally ignored because switching
// targets cannot relieve a consumer limit.
func (s *Service) RouteAvailable(providerName, model string) bool {
	return s.routeAvailableAt(providerName, model, time.Now().UTC())
}

func (s *Service) routeAvailableAt(providerName, model string, now time.Time) bool {
	if s == nil {
		return true
	}
	subjects := Subjects{Provider: providerName, Model: model}
	matching := s.matchingRules(subjects)
	if len(matching) == 0 {
		return true
	}
	return s.limiter.available(matching, now)
}

// RecordTokens adds consumed tokens to every token window matching the
// subjects. Called after responses complete, from the usage tap.
func (s *Service) RecordTokens(subjects Subjects, tokens int64, now time.Time) {
	if s == nil || tokens <= 0 {
		return
	}
	subjects, err := normalizeSubjects(subjects)
	if err != nil {
		return
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	matching := s.matchingRules(subjects)
	if len(matching) == 0 {
		return
	}
	s.limiter.recordTokens(matching, tokens, now.UTC())
}

// Statuses reports live counter state for every rule.
func (s *Service) Statuses(now time.Time) []Status {
	if s == nil {
		return nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	rules := s.Rules()
	statuses := make([]Status, 0, len(rules))
	for _, rule := range rules {
		// A per-child definition has no single aggregate counter. Child-specific
		// status is available through StatusesForUserPath.
		if rule.PerChild {
			statuses = append(statuses, Status{Rule: rule})
			continue
		}
		statuses = append(statuses, s.limiter.status(rule, now))
	}
	return statuses
}

// StatusesForUserPath reports live counter state for every user-path-scoped
// rule covering userPath (subtree match). Provider- and model-scoped rules are
// excluded: they describe shared infrastructure capacity, not the consumer.
func (s *Service) StatusesForUserPath(userPath string, now time.Time) []Status {
	if s == nil {
		return nil
	}
	userPath, err := NormalizeUserPath(userPath)
	if err != nil {
		return nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()

	var statuses []Status
	for _, rule := range s.Rules() {
		if rule.Scope != ScopeUserPath {
			continue
		}
		resolved, ok := rule.resolve(Subjects{UserPath: userPath})
		if !ok {
			continue
		}
		statuses = append(statuses, s.limiter.status(resolved, now))
	}
	return statuses
}

// ResetRule clears the live window counters for one rule.
func (s *Service) ResetRule(scope RuleScope, subject string, periodSeconds int64) error {
	if s == nil {
		return ErrUnavailable
	}
	scope, subject, err := normalizeRuleKey(scope, subject, periodSeconds)
	if err != nil {
		return err
	}
	s.limiter.reset(ruleKey{scope: scope, subject: subject, periodSeconds: periodSeconds})
	return s.persistDelete(context.Background(), scope, subject, periodSeconds)
}

// ResetAll clears every live window counter.
func (s *Service) ResetAll() error {
	if s == nil {
		return ErrUnavailable
	}
	s.limiter.resetAll()
	return s.persistDeleteAll(context.Background())
}

func (s *Service) matchingRules(subjects Subjects) []Rule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var matching []Rule
	for _, rule := range s.rules {
		if matched, ok := rule.resolve(subjects); ok {
			matching = append(matching, matched)
		}
	}
	return matching
}

// normalizeSubjects canonicalizes request subjects before matching. The user
// path arrives pre-normalized from ingress; this guards direct service use.
func normalizeSubjects(subjects Subjects) (Subjects, error) {
	if subjects.UserPath != "" {
		userPath, err := NormalizeUserPath(subjects.UserPath)
		if err != nil {
			return Subjects{}, err
		}
		subjects.UserPath = userPath
	}
	subjects.Provider = strings.ToLower(strings.TrimSpace(subjects.Provider))
	subjects.Model = strings.TrimSpace(subjects.Model)
	return subjects, nil
}

func normalizeRuleKey(scope RuleScope, subject string, periodSeconds int64) (RuleScope, string, error) {
	scope, err := NormalizeScope(string(scope))
	if err != nil {
		return "", "", err
	}
	subject, err = NormalizeSubject(scope, subject)
	if err != nil {
		return "", "", err
	}
	if err := validatePeriodSeconds(periodSeconds); err != nil {
		return "", "", err
	}
	return scope, subject, nil
}

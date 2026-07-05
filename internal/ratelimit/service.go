package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// ErrUnavailable indicates a rate limit service was used without an initialized store.
var ErrUnavailable = errors.New("rate limit service is unavailable")

type Service struct {
	store   Store
	limiter *limiter

	mu    sync.RWMutex
	rules []Rule
}

func NewService(ctx context.Context, store Store) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("rate limit store is required")
	}
	service := &Service{
		store:   store,
		limiter: newLimiter(),
	}
	if err := service.Refresh(ctx); err != nil {
		return nil, err
	}
	return service, nil
}

func (s *Service) Refresh(ctx context.Context) error {
	if s == nil || s.store == nil {
		return ErrUnavailable
	}
	rules, err := s.store.ListRules(ctx)
	if err != nil {
		return err
	}
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].UserPath == rules[j].UserPath {
			return rules[i].PeriodSeconds < rules[j].PeriodSeconds
		}
		return rules[i].UserPath < rules[j].UserPath
	})
	s.mu.Lock()
	s.rules = rules
	s.mu.Unlock()
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
	if err := s.store.UpsertRules(ctx, rules); err != nil {
		return err
	}
	return s.Refresh(ctx)
}

func (s *Service) DeleteRule(ctx context.Context, userPath string, periodSeconds int64) error {
	if s == nil || s.store == nil {
		return ErrUnavailable
	}
	userPath, err := NormalizeUserPath(userPath)
	if err != nil {
		return err
	}
	if err := validatePeriodSeconds(periodSeconds); err != nil {
		return err
	}
	if err := s.store.DeleteRule(ctx, userPath, periodSeconds); err != nil {
		return err
	}
	s.limiter.reset(ruleKey{userPath: userPath, periodSeconds: periodSeconds})
	return s.Refresh(ctx)
}

func (s *Service) ReplaceConfigRules(ctx context.Context, rules []Rule) error {
	if s == nil || s.store == nil {
		return ErrUnavailable
	}
	if err := s.store.ReplaceConfigRules(ctx, rules); err != nil {
		return err
	}
	return s.Refresh(ctx)
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

// Acquire admits or rejects a request for the user path. On rejection the
// returned error unwraps to *ExceededError. The returned reservation is
// always non-nil on success.
func (s *Service) Acquire(userPath string, now time.Time) (*Reservation, error) {
	if s == nil {
		return &Reservation{}, nil
	}
	userPath, err := NormalizeUserPath(userPath)
	if err != nil {
		return nil, err
	}
	if userPath == "" {
		userPath = "/"
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()

	matching := s.matchingRules(userPath)
	if len(matching) == 0 {
		return &Reservation{}, nil
	}
	headers, held, exceeded := s.limiter.admit(matching, now)
	if exceeded != nil {
		return nil, exceeded
	}
	return &Reservation{limiter: s.limiter, held: held, headers: headers}, nil
}

// RecordTokens adds consumed tokens to every token window matching the user
// path. Called after responses complete, from the usage tap.
func (s *Service) RecordTokens(userPath string, tokens int64, now time.Time) {
	if s == nil || tokens <= 0 {
		return
	}
	userPath, err := NormalizeUserPath(userPath)
	if err != nil {
		return
	}
	if userPath == "" {
		userPath = "/"
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	matching := s.matchingRules(userPath)
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
		statuses = append(statuses, s.limiter.status(rule, now))
	}
	return statuses
}

// ResetRule clears the live window counters for one rule.
func (s *Service) ResetRule(userPath string, periodSeconds int64) error {
	if s == nil {
		return ErrUnavailable
	}
	userPath, err := NormalizeUserPath(userPath)
	if err != nil {
		return err
	}
	s.limiter.reset(ruleKey{userPath: userPath, periodSeconds: periodSeconds})
	return nil
}

// ResetAll clears every live window counter.
func (s *Service) ResetAll() error {
	if s == nil {
		return ErrUnavailable
	}
	s.limiter.resetAll()
	return nil
}

func (s *Service) matchingRules(userPath string) []Rule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var matching []Rule
	for _, rule := range s.rules {
		if ruleAppliesToPath(rule.UserPath, userPath) {
			matching = append(matching, rule)
		}
	}
	return matching
}

package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
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
	// refreshMu serializes Refresh from the store read through the swap, so
	// a slower refresh never installs an older snapshot over a newer one.
	refreshMu sync.Mutex

	quotaTemplates bool

	// writer keys this generation's window snapshot rows, so replicas
	// sharing a store never overwrite each other. Every Service draws a new
	// one, so a restart or reload writes beside its predecessor's rows, and
	// counts them as another writer's (see peerView.sum).
	writer        string
	flushInterval time.Duration
	// persistMu serializes saves, resets of saved rows, and installing the
	// peer view with Refresh's swap; it also guards peers.
	persistMu    sync.Mutex
	lifeMu       sync.Mutex
	persistState persistState
	flushStop    chan struct{}
	flushDone    chan struct{}
	peers        peerView
	// now is the clock the peer view is kept by. Tests replace it.
	now func() time.Time

	// changed is called after every successful rule write; see
	// SetChangeNotifier.
	changed func(context.Context)
	// onRulesSwapped, when set, runs in Refresh between the swap and the
	// counter resets it implies. Tests use it.
	onRulesSwapped func()
}

// SetChangeNotifier installs fn, called after every successful write of the
// rules so other gateway instances reload them. Call it before serving;
// writes made before it was set (startup seeding) are not announced. Counter
// resets are rule writes too: they give the rule a new counter epoch, and the
// reload clears every instance's counters for it.
func (s *Service) SetChangeNotifier(fn func(context.Context)) {
	if s != nil {
		s.changed = fn
	}
}

// notifyChanged announces a successful rule write. It runs after the local
// refresh, whether or not that succeeded: the store changed either way.
func (s *Service) notifyChanged(ctx context.Context) {
	if s.changed != nil {
		s.changed(ctx)
	}
}

func NewService(ctx context.Context, store Store, options ...ServiceOption) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("rate limit store is required")
	}
	service := &Service{
		store:          store,
		limiter:        newLimiter(),
		quotaTemplates: true,
		writer:         uuid.NewString(),
		now:            time.Now,
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

// Close stops the flush loop, saves what changed since the last flush if this
// generation was started, and stops the in-memory expiry cleanup worker.
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
		s.save(context.Background())
	}
	if s.limiter != nil {
		s.limiter.close()
	}
}

func (s *Service) Refresh(ctx context.Context) error {
	if s == nil || s.store == nil {
		return ErrUnavailable
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
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
	// The swap and the counter resets it implies happen under persistMu, so
	// a flush never pairs the new rules, and with them a new counter epoch,
	// with counts the change clears, and a peer load summed under the old
	// rules never lands after the resets.
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.Lock()
	previous := s.rules
	s.rules = rules
	s.mu.Unlock()
	if s.onRulesSwapped != nil {
		s.onRulesSwapped()
	}

	// Removed definitions, shared/per-child mode changes, and definitions
	// reset, or deleted and re-added, elsewhere (a new counter epoch) drop
	// their in-memory counters, this writer's and the other writers' sums.
	// Their saved rows need no delete: they carry the old epoch, which the
	// peer view skips (see Rule.CounterEpoch), and the store collects them
	// once no writer refreshes them. Deleting them here could also take rows
	// another replica already saved under the new rule.
	next := make(map[ruleKey]Rule, len(rules))
	for _, rule := range rules {
		next[keyForRule(rule)] = rule
	}
	for _, old := range previous {
		newRule, exists := next[keyForRule(old)]
		if !exists || old.PerChild != newRule.PerChild || old.CounterEpoch != newRule.CounterEpoch {
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
	defer s.notifyChanged(ctx)
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
	defer s.notifyChanged(ctx)
	s.limiter.reset(ruleKey{scope: scope, subject: subject, periodSeconds: periodSeconds})
	// The rule is already gone from the store, so the refresh has to happen
	// either way — dropping it on a snapshot-row failure would leave the
	// deleted rule enforced in memory, which is worse than the stale row.
	// The caller still hears about the row.
	persistErr := s.persistReset(ctx, scope, subject, periodSeconds)
	if err := s.Refresh(ctx); err != nil {
		return err
	}
	return persistErr
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
	defer s.notifyChanged(ctx)
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

// ResetRule clears one rule's window counters on every instance: the store
// gives the rule a new counter epoch, and the reload that the change notifier
// triggers clears each instance's counters for it (see Refresh). The local
// counters are cleared first, so the reset holds here even when the store
// write fails.
func (s *Service) ResetRule(ctx context.Context, scope RuleScope, subject string, periodSeconds int64) error {
	if s == nil {
		return ErrUnavailable
	}
	scope, subject, err := normalizeRuleKey(scope, subject, periodSeconds)
	if err != nil {
		return err
	}
	s.limiter.reset(ruleKey{scope: scope, subject: subject, periodSeconds: periodSeconds})
	return s.announceReset(ctx, s.persistReset(ctx, scope, subject, periodSeconds))
}

// ResetAll clears every window counter on every instance, as ResetRule does
// for one rule.
func (s *Service) ResetAll(ctx context.Context) error {
	if s == nil {
		return ErrUnavailable
	}
	s.limiter.resetAll()
	return s.announceReset(ctx, s.persistResetAll(ctx))
}

// announceReset reloads the rules, with the new epoch, and announces the
// change. It does so even when the store write failed, as part of it may have
// landed, and a reload that finds nothing new changes nothing.
func (s *Service) announceReset(ctx context.Context, resetErr error) error {
	if s.store == nil {
		return resetErr
	}
	defer s.notifyChanged(ctx)
	return errors.Join(resetErr, s.Refresh(ctx))
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

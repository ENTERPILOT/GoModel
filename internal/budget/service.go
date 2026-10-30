package budget

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"
)

// ErrUnavailable indicates a budget service was used without an initialized store.
var ErrUnavailable = errors.New("budget service is unavailable")

// ErrQuotaTemplatesUnavailable indicates that a per-child template was found
// while the deployment does not have the quota-templates capability.
var ErrQuotaTemplatesUnavailable = errors.New("per-child quota templates are not enabled")

// ErrBilledBudgetsUnavailable indicates that a budget on billed cost was found
// while the deployment lacks the billed-budgets capability or has markups
// disabled.
var ErrBilledBudgetsUnavailable = errors.New("budgets on billed cost require a GoModel Pro license with the billing entitlement and pricing.markups_enabled")

// ServiceOption configures a budget service.
type ServiceOption func(*Service)

// WithQuotaTemplates controls whether per-child user-path budgets are accepted.
func WithQuotaTemplates(enabled bool) ServiceOption {
	return func(service *Service) {
		service.caps.quotaTemplates = enabled
	}
}

// WithSpendCacheTTL sets how long enforcement checks reuse a window's spend.
// Zero or negative disables the cache.
func WithSpendCacheTTL(ttl time.Duration) ServiceOption {
	return func(service *Service) {
		service.spendCacheTTL = ttl
	}
}

// WithBilledBudgets controls whether budgets on billed cost are accepted.
func WithBilledBudgets(enabled bool) ServiceOption {
	return func(service *Service) {
		service.caps.billedBudgets = enabled
	}
}

// WithBilledCacheHits reports whether response-cache hits add billed cost
// (markups enabled and bill_cache_hits on), which makes CheckCacheHit enforce
// billed budgets before a cached response is replayed.
func WithBilledCacheHits(enabled bool) ServiceOption {
	return func(service *Service) {
		service.billedCacheHits = enabled
	}
}

// WithProviderBilledCacheHits controls whether provider budgets on billed cost
// count billed response-cache hits. The default is true.
func WithProviderBilledCacheHits(enabled bool) ServiceOption {
	return func(service *Service) {
		service.providerBilledCacheHits = enabled
	}
}

// capabilities records which optional budget behaviors the deployment has.
type capabilities struct {
	quotaTemplates bool
	billedBudgets  bool
}

// validate refuses budgets being written that need a capability the
// deployment does not have.
func (c capabilities) validate(budgets []Budget) error {
	for _, item := range budgets {
		if item.PerChild && !c.quotaTemplates {
			return fmt.Errorf("%w: budget for user path %q", ErrQuotaTemplatesUnavailable, item.Subject)
		}
		if item.CostBasis == CostBasisBilled && !c.billedBudgets {
			return fmt.Errorf("%w: budget for %s", ErrBilledBudgetsUnavailable, item.SubjectLabel())
		}
	}
	return nil
}

type Service struct {
	store Store
	mu    sync.RWMutex
	// refreshMu serializes Refresh from the store read through the swap, so
	// a slower refresh never installs an older snapshot over a newer one.
	refreshMu sync.Mutex

	spendCacheTTL time.Duration
	spends        *spendCache

	budgets  []Budget
	settings Settings
	// billedConsumerBudgets is set when a user-path or label budget limits
	// billed cost, so CheckCacheHit can return without a store round trip.
	billedConsumerBudgets bool
	// downgradeWarned remembers the stored billed budgets already reported as
	// enforced on provider cost, so each is logged once.
	downgradeWarned map[string]struct{}

	caps            capabilities
	billedCacheHits bool
	// providerBilledCacheHits keeps billed cache hits in provider budgets on
	// billed cost; see config.BudgetsConfig.ProviderBilledCacheHits.
	providerBilledCacheHits bool

	// changed is called after every successful write; see SetChangeNotifier.
	changed func(context.Context)
}

// SetChangeNotifier installs fn, called after every successful write of the
// budgets or their settings so other gateway instances reload them. Call it
// before serving; writes made before it was set (startup seeding) are not
// announced.
func (s *Service) SetChangeNotifier(fn func(context.Context)) {
	if s != nil {
		s.changed = fn
	}
}

// notifyChanged announces a successful write. It runs after the local
// refresh, whether or not that succeeded: the store changed either way.
func (s *Service) notifyChanged(ctx context.Context) {
	if s.changed != nil {
		s.changed(ctx)
	}
}

func NewService(ctx context.Context, store Store, options ...ServiceOption) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("budget store is required")
	}
	service := &Service{
		store:                   store,
		settings:                DefaultSettings(),
		caps:                    capabilities{quotaTemplates: true, billedBudgets: true},
		providerBilledCacheHits: true,
		spendCacheTTL:           defaultSpendCacheTTL,
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	if service.spendCacheTTL > 0 {
		service.spends = newSpendCache(service.spendCacheTTL)
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
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	budgets, err := s.store.ListBudgets(ctx)
	if err != nil {
		return err
	}
	// Stored per-child templates still need the capability: without it their
	// limits would silently change meaning.
	if err := (capabilities{quotaTemplates: s.caps.quotaTemplates, billedBudgets: true}).validate(budgets); err != nil {
		return err
	}
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	slices.SortStableFunc(budgets, func(a, b Budget) int {
		if order := cmp.Or(
			cmp.Compare(scopeRank(a.Scope), scopeRank(b.Scope)),
			cmp.Compare(a.Subject, b.Subject),
		); order != 0 {
			return order
		}
		return cmp.Compare(b.PeriodSeconds, a.PeriodSeconds)
	})
	s.mu.Lock()
	budgets = s.downgradeUnavailableBilled(budgets)
	s.budgets = budgets
	s.settings = settings
	s.billedConsumerBudgets = slices.ContainsFunc(budgets, func(b Budget) bool {
		return b.EnforcedBasis() == CostBasisBilled && b.Scope != ScopeProvider
	})
	s.mu.Unlock()
	return nil
}

// scopeRank orders budgets for evaluation. Provider budgets come last so that,
// when CheckWithResults stops at the first exhausted budget, a consumer breach
// (which failover cannot relieve) is the one reported.
func scopeRank(scope Scope) int {
	switch scope {
	case ScopeLabel:
		return 0
	case ScopeUserPath:
		return 1
	default:
		return 2
	}
}

func (s *Service) UpsertBudgets(ctx context.Context, budgets []Budget) error {
	if s == nil || s.store == nil {
		return ErrUnavailable
	}
	if err := s.validateEdits(budgets); err != nil {
		return err
	}
	if err := s.store.UpsertBudgets(ctx, budgets); err != nil {
		return err
	}
	defer s.notifyChanged(ctx)
	return s.Refresh(ctx)
}

func (s *Service) DeleteBudget(ctx context.Context, scope Scope, subject string, periodSeconds int64) error {
	if s == nil || s.store == nil {
		return ErrUnavailable
	}
	scope, subject, err := normalizeBudgetKey(scope, subject, periodSeconds)
	if err != nil {
		return err
	}
	if err := s.store.DeleteBudget(ctx, scope, subject, periodSeconds); err != nil {
		return err
	}
	defer s.notifyChanged(ctx)
	return s.Refresh(ctx)
}

func (s *Service) ReplaceConfigBudgets(ctx context.Context, budgets []Budget) error {
	if s == nil || s.store == nil {
		return ErrUnavailable
	}
	// Config budgets are declared, not edited, so they get the full check,
	// like startup seeding.
	if err := s.caps.validate(budgets); err != nil {
		return err
	}
	if err := s.store.ReplaceConfigBudgets(ctx, budgets); err != nil {
		return err
	}
	defer s.notifyChanged(ctx)
	return s.Refresh(ctx)
}

// validateEdits refuses budgets being written that need a capability the
// deployment does not have. A budget already stored as billed keeps its basis
// while billed budgets are unavailable, so editing its amount does not switch
// it to provider cost for good.
func (s *Service) validateEdits(budgets []Budget) error {
	if s == nil {
		return nil
	}
	checked := make([]Budget, 0, len(budgets))
	for _, item := range budgets {
		if item.CostBasis == CostBasisBilled && s.StoredAsBilled(item.Scope, item.Subject, item.PeriodSeconds) {
			item.CostBasis = CostBasisProvider
		}
		checked = append(checked, item)
	}
	return s.caps.validate(checked)
}

// StoredAsBilled reports whether the budget with this key is stored on billed
// cost, whether or not billed budgets are currently available.
func (s *Service) StoredAsBilled(scope Scope, subject string, periodSeconds int64) bool {
	if s == nil {
		return false
	}
	key := budgetKey(scope, subject, periodSeconds)
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.ContainsFunc(s.budgets, func(b Budget) bool {
		return b.CostBasis == CostBasisBilled && budgetKey(b.Scope, b.Subject, b.PeriodSeconds) == key
	})
}

// downgradeUnavailableBilled enforces stored billed budgets on provider cost
// while billed budgets are unavailable (license lapsed or markups disabled),
// so the limit stays in force and the gateway still starts; restoring the
// entitlement or switching the budget to provider cost ends it. CostBasis
// keeps the stored basis, so listings show it and edits keep it. Each budget
// is logged once. The caller holds s.mu.
func (s *Service) downgradeUnavailableBilled(budgets []Budget) []Budget {
	if s.caps.billedBudgets {
		return budgets
	}
	for i := range budgets {
		if budgets[i].CostBasis != CostBasisBilled {
			continue
		}
		budgets[i].EnforcedCostBasis = CostBasisProvider
		key := budgetKey(budgets[i].Scope, budgets[i].Subject, budgets[i].PeriodSeconds)
		if _, warned := s.downgradeWarned[key]; warned {
			continue
		}
		if s.downgradeWarned == nil {
			s.downgradeWarned = make(map[string]struct{})
		}
		s.downgradeWarned[key] = struct{}{}
		slog.Warn("budget on billed cost enforced on provider cost: billed budgets need a GoModel Pro license with the billing entitlement and PRICING_MARKUPS_ENABLED=true",
			"scope", budgets[i].Scope,
			"subject", budgets[i].Subject,
			"period", PeriodLabel(budgets[i].PeriodSeconds),
		)
	}
	return budgets
}

func (s *Service) Budgets() []Budget {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Budget(nil), s.budgets...)
}

func (s *Service) Settings() Settings {
	if s == nil {
		return DefaultSettings()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

func (s *Service) SaveSettings(ctx context.Context, settings Settings) (Settings, error) {
	if s == nil || s.store == nil {
		return Settings{}, ErrUnavailable
	}
	saved, err := s.store.SaveSettings(ctx, settings)
	if err != nil {
		return Settings{}, err
	}
	defer s.notifyChanged(ctx)
	if err := s.Refresh(ctx); err != nil {
		return saved, fmt.Errorf("refresh budget service after saving settings: %w", err)
	}
	return saved, nil
}

// Statuses evaluates every configured budget without enforcing limits.
func (s *Service) Statuses(ctx context.Context, now time.Time) ([]CheckResult, error) {
	return s.statusesMatching(ctx, nil, now)
}

// StatusesFor evaluates every budget covering the request subjects without
// enforcing limits. Unlike CheckWithResults it never stops at an exhausted
// budget, so callers get the full status picture even when several are
// exceeded.
func (s *Service) StatusesFor(ctx context.Context, subjects Subjects, now time.Time) ([]CheckResult, error) {
	return s.statusesMatching(ctx, &subjects, now)
}

// statusesMatching evaluates the budgets covering subjects, or all of them
// when subjects is nil.
func (s *Service) statusesMatching(ctx context.Context, subjects *Subjects, now time.Time) ([]CheckResult, error) {
	if s == nil || s.store == nil {
		return nil, ErrUnavailable
	}
	matching, settings, now, err := s.match(subjects, now)
	if err != nil {
		return nil, err
	}
	return s.evaluate(ctx, matching, now, settings, false)
}

func (s *Service) ResetBudget(ctx context.Context, scope Scope, subject string, periodSeconds int64, at time.Time) error {
	if s == nil || s.store == nil {
		return ErrUnavailable
	}
	scope, subject, err := normalizeBudgetKey(scope, subject, periodSeconds)
	if err != nil {
		return err
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if err := s.store.ResetBudget(ctx, scope, subject, periodSeconds, at.UTC()); err != nil {
		return err
	}
	defer s.notifyChanged(ctx)
	return s.Refresh(ctx)
}

func (s *Service) ResetAll(ctx context.Context, at time.Time) error {
	if s == nil || s.store == nil {
		return ErrUnavailable
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if err := s.store.ResetAllBudgets(ctx, at.UTC()); err != nil {
		return err
	}
	defer s.notifyChanged(ctx)
	return s.Refresh(ctx)
}

func (s *Service) Check(ctx context.Context, subjects Subjects, now time.Time) error {
	_, err := s.CheckWithResults(ctx, subjects, now)
	return err
}

// CheckWithResults evaluates every budget covering the request subjects and
// stops at the first exhausted one, returning it as an ExceededError alongside
// the results evaluated so far.
func (s *Service) CheckWithResults(ctx context.Context, subjects Subjects, now time.Time) ([]CheckResult, error) {
	if s == nil || s.store == nil {
		return nil, ErrUnavailable
	}
	matching, settings, now, err := s.match(&subjects, now)
	if err != nil {
		return nil, err
	}
	if len(matching) == 0 {
		return nil, nil
	}
	results, err := s.evaluate(ctx, matching, now, settings, true)
	if err != nil {
		return results, err
	}
	for i, result := range results {
		if result.HasUsage && result.Spent >= result.Budget.Amount {
			return results[:i+1], &ExceededError{Result: result}
		}
	}
	return results, nil
}

// CheckCacheHit evaluates the budgets a response-cache hit spends against
// before the cached response is replayed. Only billed user-path and label
// budgets qualify, and only while cache hits are billed: provider cost never
// counts cache hits, and a hit is not routed to any provider. It returns
// without a store round trip when no such budget exists.
func (s *Service) CheckCacheHit(ctx context.Context, subjects Subjects, now time.Time) error {
	if s == nil || s.store == nil || !s.billedCacheHits {
		return nil
	}
	s.mu.RLock()
	billed := s.billedConsumerBudgets
	s.mu.RUnlock()
	if !billed {
		return nil
	}
	subjects.Provider = ""
	matching, settings, now, err := s.match(&subjects, now)
	if err != nil {
		return err
	}
	matching = slices.DeleteFunc(matching, func(b Budget) bool { return b.EnforcedBasis() != CostBasisBilled })
	if len(matching) == 0 {
		return nil
	}
	results, err := s.evaluate(ctx, matching, now, settings, true)
	if err != nil {
		return err
	}
	for _, result := range results {
		if result.HasUsage && result.Spent >= result.Budget.Amount {
			return &ExceededError{Result: result}
		}
	}
	return nil
}

// ProviderWithinBudget reports whether no provider budget for providerName is
// exhausted. Failover uses it to skip targets whose provider budget is spent;
// consumer budgets are deliberately ignored because switching targets cannot
// relieve them. A store failure counts as exhausted, matching admission, which
// refuses a request when the budget check fails.
func (s *Service) ProviderWithinBudget(ctx context.Context, providerName string) bool {
	if s == nil || s.store == nil || providerName == "" {
		return true
	}
	now := time.Now().UTC()
	s.mu.RLock()
	budgets := s.budgets
	settings := s.settings
	s.mu.RUnlock()

	subjects := Subjects{Provider: providerName}
	var matching []Budget
	for _, budget := range budgets {
		if budget.Scope == ScopeProvider && budget.appliesTo(subjects) {
			matching = append(matching, budget)
		}
	}
	if len(matching) == 0 {
		return true
	}
	// Failover is rare; read fresh so a cached spend cannot mask a store failure.
	results, err := s.evaluate(ctx, matching, now, settings, false)
	if err != nil {
		return false
	}
	for _, result := range results {
		if result.HasUsage && result.Spent >= result.Budget.Amount {
			return false
		}
	}
	return true
}

// match snapshots the budgets covering subjects — or all of them when subjects
// is nil — along with the reset settings and the normalized evaluation time.
func (s *Service) match(subjects *Subjects, now time.Time) ([]Budget, Settings, time.Time, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()

	// Refresh publishes a freshly built slice rather than mutating the current
	// one, so the header read under the lock stays valid afterwards and the
	// matching pass needs no copy.
	s.mu.RLock()
	budgets := s.budgets
	settings := s.settings
	s.mu.RUnlock()

	if subjects == nil {
		return slices.Clone(budgets), settings, now, nil
	}
	userPath, err := NormalizeUserPath(subjects.UserPath)
	if err != nil {
		return nil, settings, now, err
	}
	resolved := Subjects{UserPath: userPath, Labels: subjects.Labels, Provider: subjects.Provider}

	matching := make([]Budget, 0, len(budgets))
	for _, budget := range budgets {
		if matched, ok := budget.resolve(resolved); ok {
			matching = append(matching, matched)
		}
	}
	return matching, settings, now, nil
}

// evaluate resolves the active period of every budget and asks the store for
// all their spends in one round trip. Enforcement runs on every request, so the
// batched lookup is what keeps a wide match set from costing a query each, and
// useCache lets it reuse recently summed windows.
func (s *Service) evaluate(ctx context.Context, budgets []Budget, now time.Time, settings Settings, useCache bool) ([]CheckResult, error) {
	if len(budgets) == 0 {
		return []CheckResult{}, nil
	}
	results := make([]CheckResult, len(budgets))
	windows := make([]SpendWindow, 0, len(budgets))
	windowIndexes := make([]int, 0, len(budgets))
	for i, budget := range budgets {
		start, end := PeriodBounds(now, budget.PeriodSeconds, settings)
		if budget.LastResetAt != nil && budget.LastResetAt.After(start) {
			start = budget.LastResetAt.UTC()
		}
		results[i] = CheckResult{Budget: budget, PeriodStart: start, PeriodEnd: end, Remaining: budget.Amount}
		// A template shown in the global admin list has no single usage total.
		// Only resolved request/user-path checks query one child partition.
		if budget.PerChild && budget.EffectiveSubject == "" {
			continue
		}
		windows = append(windows, SpendWindow{
			Scope:            budget.Scope,
			Subject:          budget.evaluationSubject(),
			CostBasis:        budget.EnforcedBasis(),
			Start:            start,
			End:              now,
			ExcludeCacheHits: budget.Scope == ScopeProvider && !s.providerBilledCacheHits,
		})
		windowIndexes = append(windowIndexes, i)
	}
	if len(windows) == 0 {
		return results, nil
	}

	spends, err := s.sumSpend(ctx, windows, useCache)
	if err != nil {
		return nil, err
	}
	for i, spend := range spends {
		result := &results[windowIndexes[i]]
		result.Spent = spend.Total
		result.HasUsage = spend.HasUsage
		result.Remaining = result.Budget.Amount - spend.Total
	}
	return results, nil
}

// sumSpend returns the spend of every window. With useCache it queries the
// store only for windows the cache does not hold; without it every window is
// queried fresh, and the result still refreshes the cache.
func (s *Service) sumSpend(ctx context.Context, windows []SpendWindow, useCache bool) ([]Spend, error) {
	cache := s.spends
	if cache == nil {
		return s.querySpend(ctx, windows)
	}
	if !useCache {
		generation := cache.currentGeneration()
		spends, err := s.querySpend(ctx, windows)
		if err == nil {
			cache.put(windows, spends, generation)
		}
		return spends, err
	}
	spends, misses, generation := cache.get(windows)
	if len(misses) == 0 {
		return spends, nil
	}
	missed := make([]SpendWindow, len(misses))
	for i, index := range misses {
		missed[i] = windows[index]
	}
	fresh, err := s.querySpend(ctx, missed)
	if err != nil {
		return nil, err
	}
	cache.put(missed, fresh, generation)
	for i, index := range misses {
		spends[index] = fresh[i]
	}
	return spends, nil
}

func (s *Service) querySpend(ctx context.Context, windows []SpendWindow) ([]Spend, error) {
	spends, err := s.store.SumSpend(ctx, windows)
	if err != nil {
		return nil, err
	}
	if len(spends) != len(windows) {
		return nil, fmt.Errorf("budget store returned %d spends for %d windows", len(spends), len(windows))
	}
	return spends, nil
}

// UsageFlushStarted stops spend caching while the usage logger writes a
// batch, since its rows can become visible before the write returns.
func (s *Service) UsageFlushStarted() {
	if s == nil || s.spends == nil {
		return
	}
	s.spends.beginFlush()
}

// UsageFlushFinished drops cached spends once a batch write has finished, so
// the next enforcement check sums the newly written usage.
func (s *Service) UsageFlushFinished() {
	if s == nil || s.spends == nil {
		return
	}
	s.spends.endFlush()
}

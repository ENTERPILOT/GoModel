package virtualmodels

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/modelselectors"
)

// Service is the single native engine over the virtual_models store. It serves
// both redirect resolution (alias behavior) and policy authorization (access
// override behavior) from one atomically swapped in-memory snapshot.
type Service struct {
	store          Store
	catalog        Catalog
	defaultEnabled bool

	// configModels are virtual models supplied declaratively (config.yaml / env).
	// They are merged over the store rows on every refresh, override store rows of
	// the same source, and are read-only to the admin API.
	configModels []VirtualModel

	// targetCapacity optionally reports whether a concrete target currently
	// has rate-limit capacity. It steers load balancing only — capacity never
	// affects catalog membership, so a saturated target stays listed and its
	// redirects stay valid. Set once during startup, before serving.
	targetCapacity func(qualifiedModel string) bool

	// routeSelector optionally delegates target choice for redirects using
	// the adaptive strategy. Set once during startup, before serving.
	// routeSelectorName is captured panic-safe at install time so failure
	// paths never call back into extension code.
	routeSelector     ext.RouteSelector
	routeSelectorName string

	balancer  roundRobin
	sticky    stickySessions
	current   atomic.Value // snapshot
	refreshMu sync.Mutex
}

// SetTargetCapacity installs the rate-limit capacity probe consulted by load
// balancing. Must be called before the service starts resolving requests.
func (s *Service) SetTargetCapacity(capacity func(qualifiedModel string) bool) {
	if s == nil {
		return
	}
	s.targetCapacity = capacity
}

// SetRouteSelector installs the extension route selector consulted by
// redirects using the adaptive strategy. Must be called before the service
// starts resolving requests.
func (s *Service) SetRouteSelector(selector ext.RouteSelector) {
	if s == nil {
		return
	}
	s.routeSelector = selector
	s.routeSelectorName = selectorLabel(selector)
}

// selectorLabel returns the selector's name for logs, tolerating a panicking
// Name implementation, so recovery paths never re-enter extension code.
func selectorLabel(selector ext.RouteSelector) (name string) {
	if selector == nil {
		return ""
	}
	defer func() {
		if recover() != nil {
			name = "unknown"
		}
	}()
	return selector.Name()
}

// NewService creates a virtual models service backed by the store and catalog.
// defaultEnabled is the process-wide model availability default consulted when
// no policy matches.
func NewService(store Store, catalog Catalog, defaultEnabled bool) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if catalog == nil {
		return nil, fmt.Errorf("catalog is required")
	}
	service := &Service{
		store:          store,
		catalog:        catalog,
		defaultEnabled: defaultEnabled,
	}
	empty := emptySnapshot(defaultEnabled)
	service.current.Store(&empty)
	return service, nil
}

func (s *Service) snapshot() *snapshot {
	if s == nil {
		empty := emptySnapshot(true)
		return &empty
	}
	return s.current.Load().(*snapshot)
}

// Refresh reloads virtual models from storage and atomically swaps the snapshot.
func (s *Service) Refresh(ctx context.Context) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	return s.refreshLocked(ctx)
}

func (s *Service) refreshLocked(ctx context.Context) error {
	rows, err := s.store.List(ctx)
	if err != nil {
		return fmt.Errorf("list virtual models: %w", err)
	}
	next, err := buildSnapshot(s.mergeConfigModels(rows), s.defaultEnabled)
	if err != nil {
		return err
	}
	s.current.Store(&next)
	s.balancer.prune(next.redirects)
	s.sticky.prune(next.redirects)
	return nil
}

// SetConfigModels installs the declarative (config.yaml / VIRTUAL_MODELS) virtual
// models that override store rows of the same source. Call it before the first
// Refresh, then ValidateManagedConfig to reject invalid declarations at startup.
func (s *Service) SetConfigModels(models []VirtualModel) {
	cloned := make([]VirtualModel, 0, len(models))
	for _, model := range models {
		model.Managed = true
		cloned = append(cloned, model.clone())
	}
	s.configModels = cloned
}

// mergeConfigModels overlays the config-managed rows onto the store rows. A
// managed row replaces a store row of the same source, keeping config the source
// of truth for the entries it defines.
func (s *Service) mergeConfigModels(stored []VirtualModel) []VirtualModel {
	if len(s.configModels) == 0 {
		return stored
	}
	merged := make([]VirtualModel, 0, len(stored)+len(s.configModels))
	for _, row := range stored {
		if s.isManagedSource(row.Source) {
			continue
		}
		merged = append(merged, row)
	}
	return append(merged, s.configModels...)
}

// isManagedSource reports whether source is owned by a declarative config row.
func (s *Service) isManagedSource(source string) bool {
	source = strings.TrimSpace(source)
	for _, model := range s.configModels {
		if strings.TrimSpace(model.Source) == source {
			return true
		}
	}
	return false
}

// ValidateManagedConfig checks that every declarative config redirect satisfies
// the catalog-independent redirect invariants (valid selector, not solely a
// self-target, no misspelled target provider), so a malformed IaC entry fails
// startup loudly.
// Chain cycles and depth are rejected earlier, by the initial Refresh.
// declaredProviders lists the names present in the providers configuration even
// when they did not register — e.g. their credentials are unset in this
// environment — so a config shared across environments still boots; such
// targets only warn and stay unavailable. Call it once after the initial
// Refresh.
//
// It deliberately does NOT require targets to be catalog-supported: the provider
// model catalog loads asynchronously and may still be warming when this runs, and
// an unavailable target is skipped at resolve time like any other redirect target
// (the background ticker also skips this gate so a transient provider-catalog gap
// cannot freeze the snapshot). Gating startup on availability would abort an
// otherwise-valid declaration on a cold cache or a momentarily-unreachable
// provider — availability is runtime state, not a property of the declaration.
func (s *Service) ValidateManagedConfig(declaredProviders []string) error {
	declared := providerNameSet(declaredProviders)
	current := s.snapshot()
	for _, vm := range current.bySource {
		if !vm.Managed || !vm.IsRedirect() {
			continue
		}
		if err := validateRedirectStructure(vm); err != nil {
			return fmt.Errorf("load virtual model %q: %w", vm.Source, err)
		}
		if err := s.validateTargetProviders(vm, declared); err != nil {
			return fmt.Errorf("load virtual model %q: %w", vm.Source, err)
		}
	}
	return nil
}

// StartBackgroundRefresh periodically reloads virtual models until stopped.
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
					slog.Error("failed to refresh virtual models", "error", err)
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

// List returns all cached virtual models sorted by source.
func (s *Service) List() []VirtualModel {
	return s.snapshot().rows()
}

// Get returns one cached virtual model by source.
func (s *Service) Get(source string) (*VirtualModel, bool) {
	if vm, _, ok := s.snapshot().lookupCanonicalSource(source); ok {
		clone := vm.clone()
		return &clone, true
	}
	return nil, false
}

// ListViews returns all virtual models (redirects and policies) for the admin UI.
func (s *Service) ListViews() []View {
	rows := s.List()
	views := make([]View, 0, len(rows))
	for _, vm := range rows {
		view := View{
			Source:          vm.Source,
			Kind:            vm.Kind(),
			Targets:         vm.Targets,
			Strategy:        vm.Strategy,
			SessionAffinity: vm.SessionAffinity,
			Failover:        vm.Failover,
			ProviderName:    vm.ProviderName,
			Model:           vm.Model,
			UserPaths:       vm.UserPaths,
			Description:     vm.Description,
			Slowdown:        vm.Slowdown,
			Enabled:         vm.Enabled,
			Managed:         vm.Managed,
			CreatedAt:       vm.CreatedAt,
			UpdatedAt:       vm.UpdatedAt,
		}
		if vm.IsRedirect() {
			view.ResolvedModel, view.ProviderType, view.Valid = s.redirectViewResolution(vm)
		} else {
			view.ScopeKind = string(scopeKindFor(vm.Source, vm.ProviderName, vm.Model))
		}
		views = append(views, view)
	}
	return views
}

// redirectViewResolution summarizes a redirect for the admin view: a
// representative resolved model (the first available concrete model behind it,
// else the first declared one), its provider type, and whether any target is
// available. Chained virtual models are descended to their concrete models.
func (s *Service) redirectViewResolution(vm VirtualModel) (resolved, providerType string, valid bool) {
	snap := s.snapshot()
	entry, ok := snap.redirects[vm.Source]
	if !ok {
		return "", "", false
	}
	if leaves := snap.leafTargets(entry, s.catalog); len(leaves) > 0 {
		qualified := leaves[0].qualified
		return qualified, strings.TrimSpace(s.catalog.GetProviderType(qualified)), true
	}
	if representative, ok := snap.representativeLeaf(entry); ok {
		return representative.qualified, strings.TrimSpace(s.catalog.GetProviderType(representative.qualified)), false
	}
	return "", "", false
}

// Upsert validates and stores one virtual model, replacing any existing row at
// the same source even when its kind changes. A policy with no metadata whose
// access state is identical to what it would inherit is deleted instead of
// persisting a redundant row.
func (s *Service) Upsert(ctx context.Context, vm VirtualModel) error {
	if s == nil {
		return fmt.Errorf("virtual models service is required")
	}

	normalized, err := s.normalizeForUpsert(vm)
	if err != nil {
		return err
	}
	if s.isManagedSource(normalized.Source) || s.isManagedSource(vm.Source) {
		return managedSourceError(normalized.Source)
	}

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	current := s.snapshot()
	previous, canonical, existed := current.lookupCanonicalSource(normalized.Source)
	if existed {
		normalized.Source = canonical
		if normalized.CreatedAt.IsZero() {
			normalized.CreatedAt = previous.CreatedAt
		}
		if previous.Managed {
			return managedSourceError(canonical)
		}
	}

	baseRows := current.rows()
	if existed {
		baseRows = removeRow(baseRows, canonical)
	}
	redundant, err := s.policyIsRedundant(normalized, baseRows)
	if err != nil {
		return err
	}
	if redundant {
		if !existed {
			return nil
		}
		if err := s.store.Delete(ctx, canonical); err != nil {
			if errors.Is(err, ErrNotFound) {
				return ErrNotFound
			}
			return fmt.Errorf("delete redundant virtual model policy: %w", err)
		}
		return s.commitRefresh(ctx, map[string]*VirtualModel{canonical: &previous})
	}
	if err := s.validateRedirectTarget(current, normalized); err != nil {
		return err
	}
	if _, err := buildSnapshot(upsertRow(baseRows, normalized), s.defaultEnabled); err != nil {
		return fmt.Errorf("validate virtual models: %w", err)
	}

	if err := s.store.Upsert(ctx, normalized); err != nil {
		return fmt.Errorf("upsert virtual model: %w", err)
	}
	return s.commitRefresh(ctx, map[string]*VirtualModel{
		normalized.Source: priorRow(previous, existed),
	})
}

// Rename moves an existing virtual model to a new source: it stores the row
// under the new source and removes the old one, validating and refreshing like
// Upsert with rollback on failure. A no-op rename (old == new after
// normalization) delegates to Upsert. The new source must be free — renaming
// onto an existing row is rejected rather than silently overwriting it, since
// source is the primary key on every store backend.
func (s *Service) Rename(ctx context.Context, oldSource string, vm VirtualModel) error {
	if s == nil {
		return fmt.Errorf("virtual models service is required")
	}
	oldSource = strings.TrimSpace(oldSource)
	if oldSource == "" {
		return newValidationError("source is required", nil)
	}

	normalized, err := s.normalizeForUpsert(vm)
	if err != nil {
		return err
	}
	if normalized.Source == oldSource {
		// Not actually a rename; fall back to a plain update under the same key.
		return s.Upsert(ctx, vm)
	}
	if s.isManagedSource(oldSource) || s.isManagedSource(normalized.Source) || s.isManagedSource(vm.Source) {
		return managedSourceError(normalized.Source)
	}

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	current := s.snapshot()
	previous, oldExisted := current.bySource[oldSource]
	if !oldExisted {
		return ErrNotFound
	}
	if _, taken := current.bySource[normalized.Source]; taken {
		return newValidationError(fmt.Sprintf("virtual model %q already exists; choose a different source", normalized.Source), nil)
	}
	if err := rejectDependents(current, oldSource); err != nil {
		return err
	}
	baseRows := removeRow(current.rows(), oldSource)
	redundant, err := s.policyIsRedundant(normalized, baseRows)
	if err != nil {
		return err
	}
	if redundant {
		if err := s.store.Delete(ctx, oldSource); err != nil {
			if errors.Is(err, ErrNotFound) {
				return ErrNotFound
			}
			return fmt.Errorf("delete no-op renamed policy: %w", err)
		}
		return s.commitRefresh(ctx, map[string]*VirtualModel{oldSource: &previous})
	}
	if err := s.validateRedirectTarget(current, normalized); err != nil {
		return err
	}
	rows := upsertRow(baseRows, normalized)
	if _, err := buildSnapshot(rows, s.defaultEnabled); err != nil {
		return fmt.Errorf("validate virtual models: %w", err)
	}

	if err := s.store.Upsert(ctx, normalized); err != nil {
		return fmt.Errorf("upsert virtual model: %w", err)
	}
	// Restoring the new source means deleting it (it did not exist before); the
	// old source is restored to its prior row.
	prior := map[string]*VirtualModel{
		normalized.Source: nil,
		oldSource:         &previous,
	}
	if err := s.store.Delete(ctx, oldSource); err != nil {
		// The new row is in but the old one survives; undo so the rename leaves
		// no duplicate behind.
		rollbackCtx, cancel := rollbackContext()
		defer cancel()
		if rollbackErr := s.restore(rollbackCtx, prior); rollbackErr != nil {
			return fmt.Errorf("delete old virtual model: %w (rollback failed: %v)", err, rollbackErr)
		}
		return fmt.Errorf("delete old virtual model: %w", err)
	}
	return s.commitRefresh(ctx, prior)
}

// Delete removes one virtual model and refreshes the in-memory snapshot.
func (s *Service) Delete(ctx context.Context, source string) error {
	if s == nil {
		return fmt.Errorf("virtual models service is required")
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return newValidationError("source is required", nil)
	}

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	current := s.snapshot()
	previous, canonical, existed := current.lookupCanonicalSource(source)
	if !existed {
		return ErrNotFound
	}
	if previous.Managed || s.isManagedSource(canonical) {
		return managedSourceError(canonical)
	}
	if err := rejectDependents(current, canonical); err != nil {
		return err
	}
	source = canonical

	if err := s.store.Delete(ctx, source); err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("delete virtual model: %w", err)
	}
	return s.commitRefresh(ctx, map[string]*VirtualModel{source: &previous})
}

func (s *Service) policyIsRedundant(policy VirtualModel, fallbackRows []VirtualModel) (bool, error) {
	if policy.IsRedirect() {
		return false, nil
	}
	fallback, err := buildSnapshot(fallbackRows, s.defaultEnabled)
	if err != nil {
		return false, fmt.Errorf("validate inherited virtual models: %w", err)
	}
	return policyIsNoop(policy, fallback), nil
}

// policyIsNoop reports whether an otherwise empty policy changes effective
// access compared with the snapshot that would remain without that policy.
func policyIsNoop(policy VirtualModel, fallback snapshot) bool {
	if policy.IsRedirect() || len(policy.UserPaths) > 0 || strings.TrimSpace(policy.Description) != "" || policy.Slowdown != nil {
		return false
	}

	matches := func(enabled bool, userPaths []string) bool {
		return policy.Enabled == enabled && len(userPaths) == 0
	}
	selector := core.ModelSelector{Provider: policy.ProviderName, Model: policy.Model}

	switch scopeKindFor(policy.Source, policy.ProviderName, policy.Model) {
	case modelselectors.ScopeGlobal:
		return matches(fallback.defaultEnable, nil)
	case modelselectors.ScopeProvider:
		state := fallback.effectiveState(selector)
		if !matches(state.Enabled, state.UserPaths) {
			return false
		}
		// Provider-wide policies outrank model-wide policies. Removing one can
		// therefore expose any model-wide rule for this provider.
		for _, modelPolicy := range fallback.modelWide {
			if !matches(modelPolicy.Enabled, modelPolicy.UserPaths) {
				return false
			}
		}
		return true
	default:
		state := fallback.effectiveState(selector)
		return matches(state.Enabled, state.UserPaths)
	}
}

func (s *Service) normalizeForUpsert(vm VirtualModel) (VirtualModel, error) {
	if vm.IsRedirect() {
		normalized, _, err := normalizeRedirect(vm)
		return normalized, err
	}
	normalized, err := normalizePolicyInput(s.catalog, vm)
	if err != nil {
		return VirtualModel{}, err
	}
	normalized.Strategy = ""
	normalized.Description = strings.TrimSpace(normalized.Description)
	return normalized, nil
}

// validateRedirectTarget enforces redirect rules for an admin write: the
// structural invariants, a target-provider check, and a catalog-availability
// check. The admin API runs against a warm catalog, so a target it cannot serve
// is a caller mistake worth rejecting up front. Startup config validation skips
// the availability check, because the catalog may not be warm yet (see
// ValidateManagedConfig).
func (s *Service) validateRedirectTarget(current *snapshot, vm VirtualModel) error {
	if err := validateRedirectStructure(vm); err != nil {
		return err
	}
	if err := s.validateTargetProviders(vm, nil); err != nil {
		return err
	}
	if missing, ok := s.firstUnsupportedTarget(current, vm); ok {
		return newValidationError("target model not found: "+missing, nil)
	}
	return nil
}

// validateTargetProviders checks every explicitly-named target provider against
// the registered provider names — static configuration known before any model
// loads, so a misspelled provider is caught even on a cold catalog. Targets
// without an explicit provider are skipped: their model may legitimately carry
// a non-provider prefix (a slash-shaped ID like "Qwen/Qwen3-1.7B"). A name in
// declared is configured but did not register (e.g. credentials unset in this
// environment); it warns instead of failing and the target stays unavailable.
func (s *Service) validateTargetProviders(vm VirtualModel, declared map[string]struct{}) error {
	registered := providerNameSet(s.catalog.ProviderNames())
	for _, target := range vm.Targets {
		name := strings.TrimSpace(target.Provider)
		if name == "" {
			continue
		}
		if _, ok := registered[name]; ok {
			continue
		}
		if _, ok := declared[name]; ok {
			slog.Warn("virtual model target provider is configured but not registered; the target stays unavailable until its credentials resolve",
				"source", vm.Source,
				"provider", name)
			continue
		}
		return unknownTargetProviderError(name, s.catalog.ProviderNames())
	}
	return nil
}

// validateRedirectStructure enforces the catalog-INDEPENDENT redirect invariants:
// each target must parse, and a redirect cannot consist solely of itself. A
// target naming the redirect's own source stands for the concrete model it
// shadows — meaningful only next to other targets (e.g. a failover chain for a
// real model). A target may name another redirect's source (a chain); the
// wider graph rules — no cycles, at most MaxChainDepth hops — are checked by
// validateChains whenever a candidate snapshot is built. These are pure
// properties of the declaration and the redirect graph, so they hold whether
// or not the provider catalog is warm — making them safe to enforce at
// startup, before async model loading completes.
func validateRedirectStructure(vm VirtualModel) error {
	if !vm.IsRedirect() {
		return nil
	}
	for _, target := range vm.Targets {
		selector, err := target.selector()
		if err != nil {
			return newValidationError("invalid target selector: "+err.Error(), err)
		}
		if len(vm.Targets) == 1 && vm.Source == selector.QualifiedModel() {
			return newValidationError(fmt.Sprintf("virtual model %q cannot target only itself", vm.Source), nil)
		}
	}
	return nil
}

// firstUnsupportedTarget reports the first target that is neither a model the
// catalog can currently serve nor an existing virtual model, if any. Catalog
// availability is transient — it depends on async model loading and provider
// health — and is already handled at resolve time by skipping unavailable
// targets, so it gates the admin write path only, never startup.
func (s *Service) firstUnsupportedTarget(current *snapshot, vm VirtualModel) (string, bool) {
	for _, target := range vm.Targets {
		selector, err := target.selector()
		if err != nil {
			continue // selector parse errors are reported by validateRedirectStructure
		}
		qualified := selector.QualifiedModel()
		candidate := resolvedTarget{selector: selector, qualified: qualified, explicitProvider: strings.TrimSpace(target.Provider) != ""}
		if _, chained := current.chained(vm.Source, candidate); chained {
			continue
		}
		if !s.catalog.Supports(qualified) {
			return qualified, true
		}
	}
	return "", false
}

// rejectDependents refuses to remove or rename a virtual model that other
// redirects chain through, so an outer alias cannot silently lose a leg.
func rejectDependents(current *snapshot, source string) error {
	dependents := current.dependents(source)
	if len(dependents) == 0 {
		return nil
	}
	return newValidationError(fmt.Sprintf(
		"virtual model %q is a target of %s; repoint them first",
		source, strings.Join(dependents, ", ")), nil)
}

// managedSourceError is returned when the admin API tries to write a virtual
// model that is owned declaratively by config.yaml or the VIRTUAL_MODELS env var.
func managedSourceError(source string) error {
	return newValidationError(fmt.Sprintf(
		"virtual model %q is managed by config.yaml or VIRTUAL_MODELS and cannot be changed from the admin API; edit your configuration instead",
		source), nil)
}

func upsertRow(rows []VirtualModel, next VirtualModel) []VirtualModel {
	for i := range rows {
		if rows[i].Source == next.Source {
			rows[i] = next.clone()
			return rows
		}
	}
	return append(rows, next.clone())
}

func removeRow(rows []VirtualModel, source string) []VirtualModel {
	out := make([]VirtualModel, 0, len(rows))
	for _, row := range rows {
		if row.Source != source {
			out = append(out, row)
		}
	}
	return out
}

func rollbackContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// commitRefresh refreshes the snapshot after a store write succeeds and restores
// the touched rows if the refresh fails, so a failed refresh never leaves the
// store ahead of the in-memory snapshot. prior maps each touched source to its
// state before the write — a nil row means the source did not exist and is
// deleted on rollback.
func (s *Service) commitRefresh(ctx context.Context, prior map[string]*VirtualModel) error {
	if err := s.refreshLocked(ctx); err != nil {
		rollbackCtx, cancel := rollbackContext()
		defer cancel()
		if rollbackErr := s.restore(rollbackCtx, prior); rollbackErr != nil {
			return fmt.Errorf("refresh virtual models: %w (rollback failed: %v)", err, rollbackErr)
		}
		return fmt.Errorf("refresh virtual models: %w", err)
	}
	return nil
}

// restore returns each source in prior to its captured state: re-upserting a row
// that existed, or deleting one that did not (nil). It is best-effort — every
// entry is attempted and the errors are joined, so one failure never leaves the
// other touched rows unrepaired.
func (s *Service) restore(ctx context.Context, prior map[string]*VirtualModel) error {
	var restoreErr error
	for source, row := range prior {
		var err error
		if row == nil {
			// The source did not exist before; an already-absent row is the
			// intended end state, not a rollback failure.
			if err = s.store.Delete(ctx, source); errors.Is(err, ErrNotFound) {
				err = nil
			}
		} else {
			err = s.store.Upsert(ctx, *row)
		}
		if err != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("restore virtual model %q: %w", source, err))
		}
	}
	return restoreErr
}

// priorRow captures a row's pre-write state for restore: the row itself when it
// existed, or nil when it did not.
func priorRow(row VirtualModel, existed bool) *VirtualModel {
	if !existed {
		return nil
	}
	return &row
}

// ResolveUpsertEnabled returns the enabled flag an upsert should persist when
// the request may omit it: the requested value when present; otherwise the
// stored value for source (or, on a rename, for oldSource, since the new
// source does not exist yet); defaulting to true for new rows.
func (s *Service) ResolveUpsertEnabled(source, oldSource string, requested *bool) bool {
	if requested != nil {
		return *requested
	}
	if existing, ok := s.Get(source); ok && existing != nil {
		return existing.Enabled
	}
	if old := strings.TrimSpace(oldSource); old != "" {
		if existing, ok := s.Get(old); ok && existing != nil {
			return existing.Enabled
		}
	}
	return true
}

// Compile-time check that *Service satisfies the resolver, user-path resolver,
// refresh-target, exposed-model lister, and authorizer seams its consumers
// (gateway, server, batch) depend on, so a signature drift fails to compile here.
var _ interface {
	ResolveModel(core.RequestedModelSelector) (core.ModelSelector, bool, error)
	ResolveModelForUserPath(context.Context, core.RequestedModelSelector) (core.ModelSelector, bool, error)
	ResolveRefreshTarget(core.RequestedModelSelector) (core.ModelSelector, bool, error)
	ExposedModels() []core.Model
	ExposedModelsFiltered(func(core.ModelSelector) bool) []core.Model
	ExposedModelsForUserPath(string, func(core.ModelSelector) bool) []core.Model
	ValidateModelAccess(context.Context, core.ModelSelector) error
	AllowsModel(context.Context, core.ModelSelector) bool
	FilterPublicModels(context.Context, []core.Model) []core.Model
} = (*Service)(nil)

package users

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
)

const defaultRefreshInterval = time.Minute

type snapshot struct {
	byPath map[string]User
	order  []string
}

// Service keeps user policies cached in memory and evaluates model access for
// requests.
type Service struct {
	store       Store
	catalog     Catalog
	configUsers []User

	refreshMu sync.Mutex
	current   atomic.Pointer[snapshot]
}

// NewService creates a user policy service backed by the store and catalog.
func NewService(store Store, catalog Catalog) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if catalog == nil {
		return nil, fmt.Errorf("catalog is required")
	}
	service := &Service{store: store, catalog: catalog}
	service.current.Store(&snapshot{byPath: map[string]User{}})
	return service, nil
}

func (s *Service) snapshot() *snapshot {
	if s == nil {
		return &snapshot{byPath: map[string]User{}}
	}
	return s.current.Load()
}

// SetConfigUsers installs the declarative (config.yaml / USERS) policies that
// shadow store rows of the same path. Call it before the first Refresh.
func (s *Service) SetConfigUsers(users []User) {
	cloned := make([]User, 0, len(users))
	for _, user := range users {
		user.Managed = true
		user.AllowedModels = append([]string(nil), user.AllowedModels...)
		cloned = append(cloned, user)
	}
	s.configUsers = cloned
}

// ValidateManagedConfig canonicalizes every declared policy against the
// declared provider names so a misspelled selector fails startup loudly.
func (s *Service) ValidateManagedConfig(declaredProviders []string) error {
	catalog := staticCatalog(declaredProviders)
	for i := range s.configUsers {
		user := &s.configUsers[i]
		userPath, err := core.NormalizeUserPath(user.UserPath)
		if err != nil || userPath == "" {
			return fmt.Errorf("users[%d]: invalid path %q", i, user.UserPath)
		}
		user.UserPath = userPath
		allowed, err := NormalizeAllowedModels(catalog, user.AllowedModels)
		if err != nil {
			return fmt.Errorf("users[%d] (%s): %w", i, userPath, err)
		}
		user.AllowedModels = allowed
	}
	return nil
}

// Refresh reloads policies from storage and atomically swaps the snapshot.
func (s *Service) Refresh(ctx context.Context) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	rows, err := s.store.List(ctx)
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}
	next := buildSnapshot(s.mergeConfigUsers(rows))
	s.current.Store(next)
	return nil
}

func (s *Service) mergeConfigUsers(stored []User) []User {
	if len(s.configUsers) == 0 {
		return stored
	}
	managed := make(map[string]struct{}, len(s.configUsers))
	for _, user := range s.configUsers {
		managed[user.UserPath] = struct{}{}
	}
	merged := make([]User, 0, len(stored)+len(s.configUsers))
	for _, row := range stored {
		if _, shadowed := managed[row.UserPath]; shadowed {
			continue
		}
		merged = append(merged, row)
	}
	return append(merged, s.configUsers...)
}

func buildSnapshot(rows []User) *snapshot {
	next := &snapshot{byPath: make(map[string]User, len(rows)), order: make([]string, 0, len(rows))}
	for _, row := range rows {
		userPath, err := core.NormalizeUserPath(row.UserPath)
		if err != nil || userPath == "" {
			slog.Warn("skipping user policy with invalid path", "user_path", row.UserPath)
			continue
		}
		row.UserPath = userPath
		if _, exists := next.byPath[userPath]; !exists {
			next.order = append(next.order, userPath)
		}
		next.byPath[userPath] = row
	}
	sort.Strings(next.order)
	return next
}

// StartBackgroundRefresh periodically reloads policies from storage until stopped.
func (s *Service) StartBackgroundRefresh(interval time.Duration) func() {
	if interval <= 0 {
		interval = defaultRefreshInterval
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
					slog.Warn("user policy refresh failed", "error", err)
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

// List returns every cached policy sorted by path.
func (s *Service) List() []User {
	snap := s.snapshot()
	result := make([]User, 0, len(snap.order))
	for _, userPath := range snap.order {
		result = append(result, cloneUser(snap.byPath[userPath]))
	}
	return result
}

// Get returns the policy stored for exactly userPath.
func (s *Service) Get(userPath string) (User, bool) {
	userPath, err := core.NormalizeUserPath(userPath)
	if err != nil || userPath == "" {
		return User{}, false
	}
	user, ok := s.snapshot().byPath[userPath]
	if !ok {
		return User{}, false
	}
	return cloneUser(user), true
}

// Upsert validates and persists one policy, then refreshes the snapshot.
func (s *Service) Upsert(ctx context.Context, user User) (User, error) {
	if s == nil {
		return User{}, fmt.Errorf("user service is required")
	}
	userPath, err := core.NormalizeUserPath(user.UserPath)
	if err != nil {
		return User{}, newValidationError("invalid user_path", err)
	}
	if userPath == "" {
		return User{}, newValidationError("user_path is required", nil)
	}
	if existing, ok := s.snapshot().byPath[userPath]; ok && existing.Managed {
		return User{}, ErrManaged
	}
	allowed, err := NormalizeAllowedModels(s.catalog, user.AllowedModels)
	if err != nil {
		return User{}, err
	}
	now := time.Now().UTC()
	row := User{
		UserPath:      userPath,
		AllowedModels: allowed,
		Description:   strings.TrimSpace(user.Description),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if existing, ok := s.snapshot().byPath[userPath]; ok && !existing.CreatedAt.IsZero() {
		row.CreatedAt = existing.CreatedAt
	}
	if err := s.store.Upsert(ctx, row); err != nil {
		return User{}, err
	}
	// The persisted policy is applied to the live snapshot immediately, then
	// reconciled from storage best-effort: a failed post-write read must not
	// leave request authorization on the superseded policy.
	s.applyUpsert(row)
	s.refreshBestEffort(ctx, "upsert")
	stored, _ := s.Get(userPath)
	return stored, nil
}

// Delete removes one policy and refreshes the snapshot.
func (s *Service) Delete(ctx context.Context, userPath string) error {
	if s == nil {
		return fmt.Errorf("user service is required")
	}
	userPath, err := core.NormalizeUserPath(userPath)
	if err != nil {
		return newValidationError("invalid user_path", err)
	}
	if userPath == "" {
		return newValidationError("user_path is required", nil)
	}
	if existing, ok := s.snapshot().byPath[userPath]; ok && existing.Managed {
		return ErrManaged
	}
	if err := s.store.Delete(ctx, userPath); err != nil {
		return err
	}
	s.applyDelete(userPath)
	s.refreshBestEffort(ctx, "delete")
	return nil
}

func (s *Service) refreshBestEffort(ctx context.Context, operation string) {
	if err := s.Refresh(ctx); err != nil {
		slog.Warn("user policy snapshot reconciliation failed", "operation", operation, "error", err)
	}
}

// applyUpsert swaps in a snapshot with row replacing any policy of the same path.
func (s *Service) applyUpsert(row User) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	rows := make([]User, 0, len(s.snapshot().byPath)+1)
	for _, existing := range s.snapshot().byPath {
		if existing.UserPath != row.UserPath && !existing.Managed {
			rows = append(rows, existing)
		}
	}
	rows = append(rows, row)
	s.current.Store(buildSnapshot(s.mergeConfigUsers(rows)))
}

// applyDelete swaps in a snapshot without the policy at userPath.
func (s *Service) applyDelete(userPath string) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	rows := make([]User, 0, len(s.snapshot().byPath))
	for _, existing := range s.snapshot().byPath {
		if existing.UserPath != userPath && !existing.Managed {
			rows = append(rows, existing)
		}
	}
	s.current.Store(buildSnapshot(s.mergeConfigUsers(rows)))
}

// NormalizeAllowedModels canonicalizes an allowlist against the live catalog.
func (s *Service) NormalizeAllowedModels(raw []string) ([]string, error) {
	if s == nil {
		return NormalizeAllowedModels(nil, raw)
	}
	return NormalizeAllowedModels(s.catalog, raw)
}

// Constraints returns the policies that restrict userPath, root first: every
// ancestor (including the path itself) whose allowlist is non-empty.
func (s *Service) Constraints(userPath string) []User {
	snap := s.snapshot()
	ancestors := core.UserPathAncestors(userPath)
	result := make([]User, 0, len(ancestors))
	for _, ancestor := range slices.Backward(ancestors) {
		if user, ok := snap.byPath[ancestor]; ok && len(user.AllowedModels) > 0 {
			result = append(result, cloneUser(user))
		}
	}
	return result
}

// AllowsModel reports whether the request may use selector: the credential's
// own allowlist and every non-empty allowlist on the request user path and
// its ancestors must all match. Requests without a key or user path are
// unrestricted here.
func (s *Service) AllowsModel(ctx context.Context, selector core.ModelSelector) bool {
	if allowed := core.GetCredentialAllowedModels(ctx); len(allowed) > 0 && !Matches(allowed, selector) {
		return false
	}
	userPath := core.UserPathFromContext(ctx)
	if userPath == "" {
		return true
	}
	snap := s.snapshot()
	if len(snap.byPath) == 0 {
		return true
	}
	for _, ancestor := range core.UserPathAncestors(userPath) {
		user, ok := snap.byPath[ancestor]
		if !ok || len(user.AllowedModels) == 0 {
			continue
		}
		if !Matches(user.AllowedModels, selector) {
			return false
		}
	}
	return true
}

func cloneUser(user User) User {
	user.AllowedModels = append([]string(nil), user.AllowedModels...)
	return user
}

type staticCatalog []string

func (c staticCatalog) ProviderNames() []string {
	return append([]string(nil), c...)
}

package authkeys

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/enterpilot/gomodel/internal/core"
)

const defaultRefreshInterval = time.Minute

type snapshot struct {
	order        []string
	byID         map[string]AuthKey
	bySecretHash map[string]AuthKey
	activeByHash map[string]AuthKey
}

// AuthenticationResult describes one successful managed auth key lookup.
type AuthenticationResult struct {
	ID                string
	UserPath          string
	Labels            []string
	AllowedModels     []string
	DashboardAccess   bool
	PassthroughAccess bool
}

// Service keeps managed auth keys cached in memory for request authentication.
type Service struct {
	store Store
	// tokenPrefix starts every key Create issues. Authentication does not
	// depend on it, so changing it never invalidates an existing key.
	tokenPrefix string

	mu       sync.RWMutex
	snapshot snapshot
	// refreshMu serializes Refresh, from the store read through the swap, and
	// the local applies, so a slower refresh never installs an older snapshot
	// over a newer one.
	refreshMu sync.Mutex

	// changed is called after every successful write; see SetChangeNotifier.
	changed func(context.Context)
}

// SetChangeNotifier installs fn, called after every successful key write so
// other gateway instances reload the keys, a revocation included. Call it
// before serving.
func (s *Service) SetChangeNotifier(fn func(context.Context)) {
	if s != nil {
		s.changed = fn
	}
}

// Option configures a Service.
type Option func(*Service)

// WithTokenPrefix sets the prefix of newly issued keys. Empty keeps
// DefaultTokenPrefix; NewService rejects a prefix NormalizeTokenPrefix does
// not accept.
func WithTokenPrefix(prefix string) Option {
	return func(s *Service) {
		s.tokenPrefix = prefix
	}
}

// NewService creates a managed auth key service backed by storage.
func NewService(store Store, opts ...Option) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	s := &Service{
		store: store,
		snapshot: snapshot{
			order:        []string{},
			byID:         map[string]AuthKey{},
			bySecretHash: map[string]AuthKey{},
			activeByHash: map[string]AuthKey{},
		},
	}
	for _, opt := range opts {
		opt(s)
	}
	prefix, err := NormalizeTokenPrefix(s.tokenPrefix)
	if err != nil {
		return nil, fmt.Errorf("auth key prefix %w", err)
	}
	s.tokenPrefix = prefix
	return s, nil
}

// Refresh reloads keys from storage and atomically swaps the in-memory snapshot.
func (s *Service) Refresh(ctx context.Context) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	keys, err := s.store.List(ctx)
	if err != nil {
		return fmt.Errorf("list auth keys: %w", err)
	}

	now := time.Now().UTC()
	next := snapshot{
		order:        make([]string, 0, len(keys)),
		byID:         make(map[string]AuthKey, len(keys)),
		bySecretHash: make(map[string]AuthKey, len(keys)),
		activeByHash: make(map[string]AuthKey, len(keys)),
	}

	for _, key := range keys {
		key.ID = normalizeID(key.ID)
		if key.ID == "" {
			return fmt.Errorf("load auth key %q: missing id", key.Name)
		}
		next.order = append(next.order, key.ID)
		next.byID[key.ID] = key
		next.bySecretHash[key.SecretHash] = key
		if key.Active(now) {
			next.activeByHash[key.SecretHash] = key
		}
	}

	sort.Slice(next.order, func(i, j int) bool {
		left := next.byID[next.order[i]]
		right := next.byID[next.order[j]]
		if !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.After(right.CreatedAt)
		}
		if left.Name != right.Name {
			return left.Name < right.Name
		}
		return left.ID < right.ID
	})

	s.mu.Lock()
	s.snapshot = next
	s.mu.Unlock()
	return nil
}

// Enabled reports whether managed auth keys should be enforced.
func (s *Service) Enabled() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.snapshot.byID) > 0
}

// Total returns the number of persisted managed auth keys in the current snapshot.
func (s *Service) Total() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.snapshot.byID)
}

// ActiveCount returns the number of currently active auth keys.
func (s *Service) ActiveCount() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.snapshot.activeByHash)
}

// ListViews returns all cached keys in admin-facing form.
func (s *Service) ListViews() []View {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now().UTC()
	result := make([]View, 0, len(s.snapshot.order))
	for _, id := range s.snapshot.order {
		key := s.snapshot.byID[id]
		result = append(result, View{
			AuthKey: key,
			Active:  key.Active(now),
		})
	}
	return result
}

// Create issues a new managed auth key, persists it, updates the in-memory
// snapshot immediately, and then best-effort reconciles from storage.
func (s *Service) Create(ctx context.Context, input CreateInput) (*IssuedKey, error) {
	if s == nil {
		return nil, fmt.Errorf("auth key service is required")
	}

	normalized, err := normalizeCreateInput(input)
	if err != nil {
		return nil, err
	}

	value, redactedValue, secretHash, err := generateTokenMaterial(s.tokenPrefix)
	if err != nil {
		return nil, fmt.Errorf("generate auth key: %w", err)
	}

	now := time.Now().UTC()
	key := AuthKey{
		ID:                uuid.NewString(),
		Name:              normalized.Name,
		Description:       normalized.Description,
		UserPath:          normalized.UserPath,
		Labels:            normalized.Labels,
		AllowedModels:     normalized.AllowedModels,
		DashboardAccess:   normalized.DashboardAccess,
		PassthroughAccess: normalized.PassthroughAccess,
		RedactedValue:     redactedValue,
		SecretHash:        secretHash,
		Enabled:           true,
		ExpiresAt:         normalized.ExpiresAt,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	if err := s.store.Create(ctx, key); err != nil {
		return nil, fmt.Errorf("create auth key: %w", err)
	}
	s.applyUpsert(key, now)
	s.refreshBestEffort(ctx, "create")

	return &IssuedKey{
		AuthKey: key,
		Active:  key.Active(now),
		Value:   value,
	}, nil
}

// Import stores a key another gateway issued, by the hash of its token, so
// clients keep using that token. Importing the same token again updates the
// earlier import, so re-running a migration carries over later changes; a
// Disabled input only deactivates it. It returns ErrSecretHashExists only
// when the hash belongs to a key from another source.
func (s *Service) Import(ctx context.Context, input ImportInput) (*View, ImportOutcome, error) {
	if s == nil {
		return nil, ImportSkipped, fmt.Errorf("auth key service is required")
	}

	normalized, err := normalizeImportInput(input)
	if err != nil {
		return nil, ImportSkipped, err
	}
	existing, exists := s.keyBySecretHash(normalized.SecretHash)
	if !exists && normalized.Disabled {
		// Another instance may have imported it since this one last loaded
		// keys; check storage before deciding there is nothing to deactivate.
		if err := s.Refresh(ctx); err != nil {
			return nil, ImportSkipped, err
		}
		if existing, exists = s.keyBySecretHash(normalized.SecretHash); !exists {
			return nil, ImportSkipped, nil
		}
	}
	if !exists {
		view, err := s.createImported(ctx, normalized)
		if !errors.Is(err, ErrSecretHashExists) {
			return view, ImportCreated, err
		}
		// Another instance imported it first: reload and update that key.
		if err := s.Refresh(ctx); err != nil {
			return nil, ImportSkipped, err
		}
		if existing, exists = s.keyBySecretHash(normalized.SecretHash); !exists {
			return nil, ImportSkipped, ErrSecretHashExists
		}
	}
	if existing.ImportedFrom != normalized.ImportedFrom {
		return nil, ImportSkipped, ErrSecretHashExists
	}
	if normalized.Disabled {
		// Only deactivate: the key's other fields, its expiry included, stay
		// as they were, so it is never briefly less restricted.
		if existing.Enabled && existing.DeactivatedAt == nil {
			if err := s.Deactivate(ctx, existing.ID); err != nil {
				return nil, ImportSkipped, err
			}
		}
		view, err := s.viewByID(existing.ID)
		return view, ImportUpdated, err
	}
	view, err := s.updateImported(ctx, existing, normalized)
	return view, ImportUpdated, err
}

// IDBySecretHash returns the id of the key stored under hash, the SHA-256 of
// its token, which is the key an import of that token would update.
func (s *Service) IDBySecretHash(hash string) (string, bool) {
	key, ok := s.keyBySecretHash(strings.ToLower(strings.TrimSpace(hash)))
	return key.ID, ok
}

func (s *Service) keyBySecretHash(hash string) (AuthKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key, ok := s.snapshot.bySecretHash[hash]
	return key, ok
}

func (s *Service) createImported(ctx context.Context, input ImportInput) (*View, error) {
	now := time.Now().UTC()
	key := AuthKey{
		ID:                uuid.NewString(),
		Name:              input.Name,
		Description:       input.Description,
		UserPath:          input.UserPath,
		Labels:            input.Labels,
		AllowedModels:     input.AllowedModels,
		DashboardAccess:   input.DashboardAccess,
		PassthroughAccess: input.PassthroughAccess,
		RedactedValue:     input.RedactedValue,
		SecretHash:        input.SecretHash,
		ImportedFrom:      input.ImportedFrom,
		Enabled:           true,
		ExpiresAt:         input.ExpiresAt,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := s.store.Create(ctx, key); err != nil {
		if errors.Is(err, ErrSecretHashExists) {
			return nil, ErrSecretHashExists
		}
		return nil, fmt.Errorf("import auth key: %w", err)
	}
	s.applyUpsert(key, now)
	s.refreshBestEffort(ctx, "import")
	return &View{AuthKey: key, Active: key.Active(now)}, nil
}

// updateImported brings an earlier import up to date. Dashboard access,
// passthrough access, and a deactivation made in GoModel are kept: the import never widens access an
// administrator narrowed.
func (s *Service) updateImported(ctx context.Context, key AuthKey, input ImportInput) (*View, error) {
	now := time.Now().UTC()
	key.Name = input.Name
	key.Description = input.Description
	key.UserPath = input.UserPath
	key.Labels = input.Labels
	key.AllowedModels = input.AllowedModels
	key.RedactedValue = input.RedactedValue
	key.ExpiresAt = input.ExpiresAt
	key.UpdatedAt = now
	if err := s.store.UpdateImported(ctx, key); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("update imported auth key: %w", err)
	}
	s.applyUpsert(key, now)
	s.refreshBestEffort(ctx, "import")
	return s.viewByID(key.ID)
}

// UpdateName renames a managed auth key, updates the in-memory snapshot
// immediately, best-effort reconciles from storage, and returns the updated
// admin-facing view. The name is normalized as on create and need not be
// unique.
func (s *Service) UpdateName(ctx context.Context, id, name string) (*View, error) {
	if s == nil {
		return nil, fmt.Errorf("auth key service is required")
	}
	id = normalizeID(id)
	if id == "" {
		return nil, newValidationError("auth key id is required", nil)
	}
	name, err := normalizeName(name)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	if err := s.store.UpdateName(ctx, id, name, now); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("update auth key name: %w", err)
	}
	s.applyKeyUpdate(id, now, func(key *AuthKey) {
		key.Name = name
	})
	s.refreshBestEffort(ctx, "update-name")
	return s.viewByID(id)
}

// UpdateLabels replaces a managed auth key's labels, updates the in-memory
// snapshot immediately, best-effort reconciles from storage, and returns the
// updated admin-facing view. Passing no labels clears them.
func (s *Service) UpdateLabels(ctx context.Context, id string, labels []string) (*View, error) {
	if s == nil {
		return nil, fmt.Errorf("auth key service is required")
	}
	id = normalizeID(id)
	if id == "" {
		return nil, newValidationError("auth key id is required", nil)
	}
	labels = core.MergeLabels(labels)

	now := time.Now().UTC()
	if err := s.store.UpdateLabels(ctx, id, labels, now); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("update auth key labels: %w", err)
	}
	s.applyKeyUpdate(id, now, func(key *AuthKey) {
		key.Labels = labels
	})
	s.refreshBestEffort(ctx, "update-labels")
	return s.viewByID(id)
}

// UpdateAllowedModels replaces a managed auth key's model allowlist, updates
// the in-memory snapshot immediately, best-effort reconciles from storage, and
// returns the updated admin-facing view. Passing no selectors lifts the
// key-level restriction.
func (s *Service) UpdateAllowedModels(ctx context.Context, id string, allowedModels []string) (*View, error) {
	if s == nil {
		return nil, fmt.Errorf("auth key service is required")
	}
	id = normalizeID(id)
	if id == "" {
		return nil, newValidationError("auth key id is required", nil)
	}
	allowedModels = NormalizeAllowedModels(allowedModels)

	now := time.Now().UTC()
	if err := s.store.UpdateAllowedModels(ctx, id, allowedModels, now); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("update auth key allowed models: %w", err)
	}
	s.applyKeyUpdate(id, now, func(key *AuthKey) {
		key.AllowedModels = allowedModels
	})
	s.refreshBestEffort(ctx, "update-allowed-models")
	return s.viewByID(id)
}

// UpdateDashboardAccess grants or revokes a managed auth key's admin API and
// dashboard access, updates the in-memory snapshot immediately, best-effort
// reconciles from storage, and returns the updated admin-facing view.
func (s *Service) UpdateDashboardAccess(ctx context.Context, id string, allowed bool) (*View, error) {
	if s == nil {
		return nil, fmt.Errorf("auth key service is required")
	}
	return s.updateAccess(ctx, id, "dashboard access", allowed, s.store.UpdateDashboardAccess, func(key *AuthKey) {
		key.DashboardAccess = allowed
	})
}

// UpdatePassthroughAccess grants or revokes a managed auth key's access to the
// provider passthrough routes, the same way UpdateDashboardAccess does for
// dashboard access.
func (s *Service) UpdatePassthroughAccess(ctx context.Context, id string, allowed bool) (*View, error) {
	if s == nil {
		return nil, fmt.Errorf("auth key service is required")
	}
	return s.updateAccess(ctx, id, "passthrough access", allowed, s.store.UpdatePassthroughAccess, func(key *AuthKey) {
		key.PassthroughAccess = allowed
	})
}

// updateAccess persists one access flag with save, mirrors it into the cached
// key with apply, best-effort reconciles from storage, and returns the
// updated view. name labels errors and the reconcile operation.
func (s *Service) updateAccess(
	ctx context.Context,
	id, name string,
	allowed bool,
	save func(ctx context.Context, id string, allowed bool, now time.Time) error,
	apply func(*AuthKey),
) (*View, error) {
	id = normalizeID(id)
	if id == "" {
		return nil, newValidationError("auth key id is required", nil)
	}

	now := time.Now().UTC()
	if err := save(ctx, id, allowed, now); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("update auth key %s: %w", name, err)
	}
	s.applyKeyUpdate(id, now, apply)
	s.refreshBestEffort(ctx, "update-"+strings.ReplaceAll(name, " ", "-"))
	return s.viewByID(id)
}

// View returns the admin-facing view of one cached key, or ErrNotFound.
func (s *Service) View(id string) (*View, error) {
	if s == nil {
		return nil, ErrNotFound
	}
	return s.viewByID(normalizeID(id))
}

// viewByID returns the admin-facing view of one cached key.
func (s *Service) viewByID(id string) (*View, error) {
	s.mu.RLock()
	key, exists := s.snapshot.byID[id]
	s.mu.RUnlock()
	if !exists {
		return nil, ErrNotFound
	}
	return &View{
		AuthKey: key,
		Active:  key.Active(time.Now().UTC()),
	}, nil
}

// Deactivate marks a managed auth key inactive while preserving its record and
// best-effort reconciles the snapshot from storage afterward.
func (s *Service) Deactivate(ctx context.Context, id string) error {
	if s == nil {
		return fmt.Errorf("auth key service is required")
	}
	id = normalizeID(id)
	if id == "" {
		return newValidationError("auth key id is required", nil)
	}

	now := time.Now().UTC()
	if err := s.store.Deactivate(ctx, id, now); err != nil {
		return fmt.Errorf("deactivate auth key: %w", err)
	}
	s.applyDeactivate(id, now)
	s.refreshBestEffort(ctx, "deactivate")
	return nil
}

// Authenticate validates a presented bearer token against the in-memory snapshot
// and returns the matched auth key metadata on success.
func (s *Service) Authenticate(_ context.Context, token string) (AuthenticationResult, error) {
	if s == nil {
		return AuthenticationResult{}, ErrInvalidToken
	}

	token = strings.TrimSpace(token)
	now := time.Now().UTC()

	// A token only matches keys hashed the way the candidate was: a GoModel
	// secret only keys GoModel issued, a whole LiteLLM token only imported
	// keys. So neither kind can ever pass for the other, even when a custom
	// prefix starts with "sk-" like LiteLLM tokens do.
	legacy, fixed := issuedTokenSecrets(token)
	for _, secret := range [...]string{legacy, fixed} {
		if secret == "" {
			continue
		}
		if key, ok := s.keyBySecretHash(hashSecret(secret)); ok && key.ImportedFrom == "" {
			return authenticateKey(key, now)
		}
	}
	if looksLikeLiteLLMToken(token) {
		if key, ok := s.keyBySecretHash(hashSecret(token)); ok && key.ImportedFrom == ImportedFromLiteLLM {
			return authenticateKey(key, now)
		}
	}
	return AuthenticationResult{}, ErrInvalidToken
}

// StartBackgroundRefresh periodically reloads auth keys from storage until stopped.
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
				_ = s.Refresh(refreshCtx)
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

func authenticateKey(key AuthKey, now time.Time) (AuthenticationResult, error) {
	if !key.Enabled || key.DeactivatedAt != nil {
		return AuthenticationResult{}, ErrInactive
	}
	if key.ExpiresAt != nil && !key.ExpiresAt.After(now) {
		return AuthenticationResult{}, ErrExpired
	}
	if strings.TrimSpace(key.ID) == "" {
		return AuthenticationResult{}, ErrInvalidToken
	}
	return AuthenticationResult{
		ID:                key.ID,
		UserPath:          strings.TrimSpace(key.UserPath),
		Labels:            key.Labels,
		AllowedModels:     key.AllowedModels,
		DashboardAccess:   key.DashboardAccess,
		PassthroughAccess: key.PassthroughAccess,
	}, nil
}

// refreshBestEffort runs after every successful write: it reconciles the
// snapshot from storage and announces the change to other instances.
func (s *Service) refreshBestEffort(ctx context.Context, operation string) {
	if err := s.Refresh(ctx); err != nil {
		slog.Warn("auth key snapshot reconciliation failed", "operation", operation, "error", err)
	}
	if s.changed != nil {
		s.changed(ctx)
	}
}

func (s *Service) applyUpsert(key AuthKey, now time.Time) {
	if s == nil {
		return
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	next := cloneSnapshot(s.snapshot)
	if previous, exists := next.byID[key.ID]; exists && previous.SecretHash != "" && previous.SecretHash != key.SecretHash {
		delete(next.bySecretHash, previous.SecretHash)
		delete(next.activeByHash, previous.SecretHash)
	}
	if _, exists := next.byID[key.ID]; !exists {
		next.order = append(next.order, key.ID)
	}
	next.byID[key.ID] = key
	next.bySecretHash[key.SecretHash] = key
	if key.Active(now) {
		next.activeByHash[key.SecretHash] = key
	} else {
		delete(next.activeByHash, key.SecretHash)
	}
	sortSnapshotOrder(&next)
	s.snapshot = next
}

// applyKeyUpdate mutates one cached key in a cloned snapshot and swaps it in.
func (s *Service) applyKeyUpdate(id string, now time.Time, mutate func(*AuthKey)) {
	if s == nil {
		return
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	next := cloneSnapshot(s.snapshot)
	key, exists := next.byID[id]
	if !exists {
		s.snapshot = next
		return
	}
	mutate(&key)
	key.UpdatedAt = now.UTC()
	next.byID[id] = key
	next.bySecretHash[key.SecretHash] = key
	if _, active := next.activeByHash[key.SecretHash]; active {
		next.activeByHash[key.SecretHash] = key
	}
	s.snapshot = next
}

func (s *Service) applyDeactivate(id string, now time.Time) {
	if s == nil {
		return
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	next := cloneSnapshot(s.snapshot)
	key, exists := next.byID[id]
	if !exists {
		s.snapshot = next
		return
	}
	key.Enabled = false
	key.UpdatedAt = now.UTC()
	if key.DeactivatedAt == nil {
		deactivatedAt := now.UTC()
		key.DeactivatedAt = &deactivatedAt
	}
	next.byID[id] = key
	next.bySecretHash[key.SecretHash] = key
	delete(next.activeByHash, key.SecretHash)
	s.snapshot = next
}

func cloneSnapshot(src snapshot) snapshot {
	next := snapshot{
		order:        append([]string(nil), src.order...),
		byID:         make(map[string]AuthKey, len(src.byID)),
		bySecretHash: make(map[string]AuthKey, len(src.bySecretHash)),
		activeByHash: make(map[string]AuthKey, len(src.activeByHash)),
	}
	maps.Copy(next.byID, src.byID)
	maps.Copy(next.bySecretHash, src.bySecretHash)
	maps.Copy(next.activeByHash, src.activeByHash)
	return next
}

func sortSnapshotOrder(next *snapshot) {
	sort.Slice(next.order, func(i, j int) bool {
		left := next.byID[next.order[i]]
		right := next.byID[next.order[j]]
		if !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.After(right.CreatedAt)
		}
		if left.Name != right.Name {
			return left.Name < right.Name
		}
		return left.ID < right.ID
	})
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

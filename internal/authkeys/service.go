package authkeys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
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
	ID              string
	UserPath        string
	UserID          string
	Labels          []string
	DashboardAccess bool
}

// Service keeps managed auth keys cached in memory for request authentication.
type Service struct {
	store Store

	// userResolver returns the current user path of a registered user id.
	// Installed at startup from the users registry; nil when the registry is
	// unavailable, in which case user-bound keys fall back to the path
	// snapshot stored on the key.
	userResolver func(userID string) (string, bool)

	mu       sync.RWMutex
	snapshot snapshot
}

// SetUserResolver installs the lookup that maps a registered user id to the
// user's current path. Call once during startup, before serving requests.
func (s *Service) SetUserResolver(resolver func(userID string) (string, bool)) {
	if s == nil {
		return
	}
	s.userResolver = resolver
}

// resolveUserPath returns the live path of a user-bound key, or "", false when
// no resolver is installed or the user is gone.
func (s *Service) resolveUserPath(userID string) (string, bool) {
	if s == nil || s.userResolver == nil || userID == "" {
		return "", false
	}
	return s.userResolver(userID)
}

// NewService creates a managed auth key service backed by storage.
func NewService(store Store) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	return &Service{
		store: store,
		snapshot: snapshot{
			order:        []string{},
			byID:         map[string]AuthKey{},
			bySecretHash: map[string]AuthKey{},
			activeByHash: map[string]AuthKey{},
		},
	}, nil
}

// Refresh reloads keys from storage and atomically swaps the in-memory snapshot.
func (s *Service) Refresh(ctx context.Context) error {
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
		if path, ok := s.resolveUserPath(key.UserID); ok {
			key.UserPath = path
		}
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
	if normalized.UserID != "" {
		path, ok := s.resolveUserPath(normalized.UserID)
		if !ok {
			return nil, newValidationError("unknown user_id: "+normalized.UserID, nil)
		}
		// Snapshot the current path so the key keeps a usable scope if the
		// user registry is ever unavailable or the user is deleted.
		normalized.UserPath = path
	}

	value, redactedValue, secretHash, err := generateTokenMaterial()
	if err != nil {
		return nil, fmt.Errorf("generate auth key: %w", err)
	}

	now := time.Now().UTC()
	key := AuthKey{
		ID:              uuid.NewString(),
		Name:            normalized.Name,
		Description:     normalized.Description,
		UserPath:        normalized.UserPath,
		UserID:          normalized.UserID,
		Labels:          normalized.Labels,
		DashboardAccess: normalized.DashboardAccess,
		RedactedValue:   redactedValue,
		SecretHash:      secretHash,
		Enabled:         true,
		ExpiresAt:       normalized.ExpiresAt,
		CreatedAt:       now,
		UpdatedAt:       now,
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

// UpdateDashboardAccess grants or revokes a managed auth key's admin API and
// dashboard access, updates the in-memory snapshot immediately, best-effort
// reconciles from storage, and returns the updated admin-facing view.
func (s *Service) UpdateDashboardAccess(ctx context.Context, id string, allowed bool) (*View, error) {
	if s == nil {
		return nil, fmt.Errorf("auth key service is required")
	}
	id = normalizeID(id)
	if id == "" {
		return nil, newValidationError("auth key id is required", nil)
	}

	now := time.Now().UTC()
	if err := s.store.UpdateDashboardAccess(ctx, id, allowed, now); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("update auth key dashboard access: %w", err)
	}
	s.applyKeyUpdate(id, now, func(key *AuthKey) {
		key.DashboardAccess = allowed
	})
	s.refreshBestEffort(ctx, "update-dashboard-access")
	return s.viewByID(id)
}

// UpdateUserBinding binds a managed auth key to a registered user, or unbinds
// it when userID is empty. Binding snapshots the user's current derived path;
// unbinding freezes the key at the path it currently resolves to, so its
// scope stays put instead of snapping back to a stale snapshot.
func (s *Service) UpdateUserBinding(ctx context.Context, id, userID string) (*View, error) {
	if s == nil {
		return nil, fmt.Errorf("auth key service is required")
	}
	id = normalizeID(id)
	if id == "" {
		return nil, newValidationError("auth key id is required", nil)
	}
	userID = strings.TrimSpace(userID)

	var userPath string
	if userID != "" {
		path, ok := s.resolveUserPath(userID)
		if !ok {
			return nil, newValidationError("unknown user_id: "+userID, nil)
		}
		userPath = path
	} else {
		view, err := s.viewByID(id)
		if err != nil {
			return nil, err
		}
		userPath = view.UserPath
		if path, ok := s.resolveUserPath(view.UserID); ok {
			userPath = path
		}
	}

	now := time.Now().UTC()
	if err := s.store.UpdateUserBinding(ctx, id, userID, userPath, now); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("update auth key user binding: %w", err)
	}
	s.applyKeyUpdate(id, now, func(key *AuthKey) {
		key.UserID = userID
		key.UserPath = userPath
	})
	s.refreshBestEffort(ctx, "update-user-binding")
	return s.viewByID(id)
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

	secret, err := parseTokenSecret(token)
	if err != nil {
		return AuthenticationResult{}, err
	}
	secretHash := hashSecret(secret)
	now := time.Now().UTC()

	s.mu.RLock()
	active, ok := s.snapshot.activeByHash[secretHash]
	if ok {
		s.mu.RUnlock()
		return s.authenticateKey(active, now)
	}
	key, exists := s.snapshot.bySecretHash[secretHash]
	s.mu.RUnlock()
	if !exists {
		return AuthenticationResult{}, ErrInvalidToken
	}
	return s.authenticateKey(key, now)
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

func (s *Service) authenticateKey(key AuthKey, now time.Time) (AuthenticationResult, error) {
	if !key.Enabled || key.DeactivatedAt != nil {
		return AuthenticationResult{}, ErrInactive
	}
	if key.ExpiresAt != nil && !key.ExpiresAt.After(now) {
		return AuthenticationResult{}, ErrExpired
	}
	if strings.TrimSpace(key.ID) == "" {
		return AuthenticationResult{}, ErrInvalidToken
	}
	userPath := strings.TrimSpace(key.UserPath)
	if path, ok := s.resolveUserPath(key.UserID); ok {
		// A user-bound key follows the user's current path, so renaming the
		// user re-scopes the key immediately. The stored path is only the
		// fallback for a missing user or registry.
		userPath = path
	}
	return AuthenticationResult{
		ID:              key.ID,
		UserPath:        userPath,
		UserID:          key.UserID,
		Labels:          key.Labels,
		DashboardAccess: key.DashboardAccess,
	}, nil
}

func (s *Service) refreshBestEffort(ctx context.Context, operation string) {
	if err := s.Refresh(ctx); err != nil {
		slog.Warn("auth key snapshot reconciliation failed", "operation", operation, "error", err)
	}
}

func (s *Service) applyUpsert(key AuthKey, now time.Time) {
	if s == nil {
		return
	}
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

func generateTokenMaterial() (value string, redactedValue string, secretHash string, err error) {
	secretBytesBuf := make([]byte, secretBytes)
	if _, err := rand.Read(secretBytesBuf); err != nil {
		return "", "", "", err
	}
	secret := base64.RawURLEncoding.EncodeToString(secretBytesBuf)
	value = TokenPrefix + secret
	return value, redactTokenValue(value), hashSecret(secret), nil
}

func parseTokenSecret(token string) (string, error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, TokenPrefix) {
		return "", ErrInvalidToken
	}
	secret := strings.TrimPrefix(token, TokenPrefix)
	if secret == "" {
		return "", ErrInvalidToken
	}
	return secret, nil
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func redactTokenValue(value string) string {
	if len(value) <= len(TokenPrefix)+4 {
		return TokenPrefix + "..."
	}
	return TokenPrefix + "..." + value[len(value)-4:]
}

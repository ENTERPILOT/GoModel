package authkeys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const defaultRefreshInterval = time.Minute

type snapshot struct {
	order        []string
	byID         map[string]AuthKey
	bySecretHash map[string]AuthKey
	activeByHash map[string]AuthKey
}

// Service keeps managed auth keys cached in memory for request authentication.
type Service struct {
	store Store

	mu       sync.RWMutex
	snapshot snapshot
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
		result = append(result, View{
			AuthKey: key,
			Active:  key.Active(now),
		})
	}
	return result
}

// Create issues a new managed auth key, persists it, refreshes the snapshot,
// and returns the plaintext token once.
func (s *Service) Create(ctx context.Context, input CreateInput) (*IssuedKey, error) {
	if s == nil {
		return nil, fmt.Errorf("auth key service is required")
	}

	normalized, err := normalizeCreateInput(input)
	if err != nil {
		return nil, err
	}

	value, redactedValue, secretHash, err := generateTokenMaterial()
	if err != nil {
		return nil, fmt.Errorf("generate auth key: %w", err)
	}

	now := time.Now().UTC()
	key := AuthKey{
		ID:            uuid.NewString(),
		Name:          normalized.Name,
		Description:   normalized.Description,
		RedactedValue: redactedValue,
		SecretHash:    secretHash,
		Enabled:       true,
		ExpiresAt:     normalized.ExpiresAt,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	if err := s.store.Create(ctx, key); err != nil {
		return nil, fmt.Errorf("create auth key: %w", err)
	}
	if err := s.Refresh(ctx); err != nil {
		return nil, fmt.Errorf("refresh auth keys: %w", err)
	}

	return &IssuedKey{
		View: View{
			AuthKey: key,
			Active:  key.Active(now),
		},
		Value: value,
	}, nil
}

// Deactivate marks a managed auth key inactive while preserving its record.
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
	if err := s.Refresh(ctx); err != nil {
		return fmt.Errorf("refresh auth keys: %w", err)
	}
	return nil
}

// Authenticate validates a presented bearer token against the in-memory snapshot
// and returns the internal auth key id on success.
func (s *Service) Authenticate(_ context.Context, token string) (string, error) {
	if s == nil {
		return "", ErrInvalidToken
	}

	secret, err := parseTokenSecret(token)
	if err != nil {
		return "", err
	}
	secretHash := hashSecret(secret)

	s.mu.RLock()
	active, ok := s.snapshot.activeByHash[secretHash]
	if ok {
		s.mu.RUnlock()
		return active.ID, nil
	}
	key, exists := s.snapshot.bySecretHash[secretHash]
	s.mu.RUnlock()
	if !exists {
		return "", ErrInvalidToken
	}
	if key.DeactivatedAt != nil || !key.Enabled {
		return "", ErrInactive
	}
	if key.ExpiresAt != nil && !key.ExpiresAt.After(time.Now().UTC()) {
		return "", ErrExpired
	}
	return "", ErrInvalidToken
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

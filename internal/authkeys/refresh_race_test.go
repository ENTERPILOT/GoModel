package authkeys

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gatedKeyStore holds its next List, once armed, after it read the keys and
// until release is closed. With listErr set, every List fails instead.
// deactivated, when set, is closed once a deactivation reached the store.
type gatedKeyStore struct {
	*testStore
	mu          sync.Mutex
	armed       bool
	started     chan struct{}
	release     chan struct{}
	deactivated chan struct{}
	listErr     error
}

func (s *gatedKeyStore) List(ctx context.Context) ([]AuthKey, error) {
	s.mu.Lock()
	if s.listErr != nil {
		defer s.mu.Unlock()
		return nil, s.listErr
	}
	keys, err := s.testStore.List(ctx)
	hold := s.armed
	s.armed = false
	s.mu.Unlock()
	if hold {
		close(s.started)
		<-s.release
	}
	return keys, err
}

func (s *gatedKeyStore) Deactivate(ctx context.Context, id string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.testStore.Deactivate(ctx, id, now)
	if s.deactivated != nil {
		close(s.deactivated)
	}
	return err
}

func (s *gatedKeyStore) put(key AuthKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[key.ID] = key
}

// A refresh that read the store before a deactivation must not install the
// still-active key over a refresh that read after it.
func TestServiceRefreshNeverInstallsAnOlderSnapshot(t *testing.T) {
	ctx := context.Background()
	key := newTestKey("key-1", time.Now().UTC().Add(-time.Hour))
	store := &gatedKeyStore{testStore: newTestStore(key)}
	service, err := NewService(store)
	require.NoError(t, err)
	require.NoError(t, service.Refresh(ctx))
	require.Equal(t, 1, service.ActiveCount())

	store.started, store.release = make(chan struct{}), make(chan struct{})
	store.armed = true
	slow := make(chan error, 1)
	go func() { slow <- service.Refresh(ctx) }()
	<-store.started

	deactivated := time.Now().UTC()
	key.DeactivatedAt = &deactivated
	store.put(key)
	fast := make(chan error, 1)
	go func() { fast <- service.Refresh(ctx) }()
	select {
	case err := <-fast:
		fast <- err
	case <-time.After(100 * time.Millisecond):
	}
	close(store.release)
	require.NoError(t, <-slow)
	require.NoError(t, <-fast)

	assert.Zero(t, service.ActiveCount())
}

// A refresh that read the store before a local deactivation must not install
// the still-active key over it, even when the deactivation's own follow-up
// reconciliation fails.
func TestServiceLocalDeactivateIsNotOverwrittenByAnOlderRefresh(t *testing.T) {
	ctx := context.Background()
	key := newTestKey("key-1", time.Now().UTC().Add(-time.Hour))
	store := &gatedKeyStore{testStore: newTestStore(key)}
	service, err := NewService(store)
	require.NoError(t, err)
	require.NoError(t, service.Refresh(ctx))
	require.Equal(t, 1, service.ActiveCount())

	store.started, store.release = make(chan struct{}), make(chan struct{})
	store.deactivated = make(chan struct{})
	store.armed = true
	slow := make(chan error, 1)
	go func() { slow <- service.Refresh(ctx) }()
	<-store.started

	deactivate := make(chan error, 1)
	go func() { deactivate <- service.Deactivate(ctx, key.ID) }()
	// The deactivation is in the store; applying it locally now queues
	// behind the refresh that read the key as still active.
	<-store.deactivated
	store.mu.Lock()
	store.listErr = errors.New("database unavailable")
	store.mu.Unlock()
	close(store.release)
	require.NoError(t, <-slow)
	require.NoError(t, <-deactivate)

	assert.Zero(t, service.ActiveCount(), "the deactivated key must stay rejected")
}

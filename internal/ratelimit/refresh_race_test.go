package ratelimit

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gatedRuleStore holds its next ListRules, once armed, after it read the
// rules and until release is closed.
type gatedRuleStore struct {
	*memStore
	mu      sync.Mutex
	armed   bool
	started chan struct{}
	release chan struct{}
}

func (s *gatedRuleStore) ListRules(context.Context) ([]Rule, error) {
	s.mu.Lock()
	rules := append([]Rule(nil), s.rules...)
	hold := s.armed
	s.armed = false
	s.mu.Unlock()
	if hold {
		close(s.started)
		<-s.release
	}
	return rules, nil
}

func (s *gatedRuleStore) setRules(rules []Rule) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = rules
}

// A refresh that read the store before a write must not install its older
// rules over those of a refresh that read after it.
func TestServiceRefreshNeverInstallsAnOlderSnapshot(t *testing.T) {
	ctx := context.Background()
	olderLimit, newerLimit := int64(5), int64(50)
	older := Rule{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: 60, MaxRequests: &olderLimit}
	newer := older
	newer.MaxRequests = &newerLimit
	store := &gatedRuleStore{memStore: &memStore{rules: []Rule{older}}}
	service, err := NewService(ctx, store)
	require.NoError(t, err)
	t.Cleanup(service.Close)

	store.started, store.release = make(chan struct{}), make(chan struct{})
	store.armed = true
	slow := make(chan error, 1)
	go func() { slow <- service.Refresh(ctx) }()
	<-store.started

	store.setRules([]Rule{newer})
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

	rules := service.Rules()
	require.Len(t, rules, 1)
	require.NotNil(t, rules[0].MaxRequests)
	assert.Equal(t, int64(50), *rules[0].MaxRequests)
}

package budget

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gatedBudgetStore holds its next ListBudgets, once armed, after it read the
// budgets and until release is closed.
type gatedBudgetStore struct {
	*fakeStore
	mu      sync.Mutex
	armed   bool
	started chan struct{}
	release chan struct{}
}

func (s *gatedBudgetStore) ListBudgets(context.Context) ([]Budget, error) {
	s.mu.Lock()
	budgets := append([]Budget(nil), s.budgets...)
	hold := s.armed
	s.armed = false
	s.mu.Unlock()
	if hold {
		close(s.started)
		<-s.release
	}
	return budgets, nil
}

func (s *gatedBudgetStore) setBudgets(budgets []Budget) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.budgets = budgets
}

// A refresh that read the store before a write must not install its older
// budgets over those of a refresh that read after it.
func TestServiceRefreshNeverInstallsAnOlderSnapshot(t *testing.T) {
	ctx := context.Background()
	older := Budget{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: 86400, Amount: 5}
	newer := older
	newer.Amount = 50
	store := &gatedBudgetStore{fakeStore: &fakeStore{budgets: []Budget{older}}}
	service, err := NewService(ctx, store)
	require.NoError(t, err)

	store.started, store.release = make(chan struct{}), make(chan struct{})
	store.armed = true
	slow := make(chan error, 1)
	go func() { slow <- service.Refresh(ctx) }()
	<-store.started

	store.setBudgets([]Budget{newer})
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

	budgets := service.Budgets()
	require.Len(t, budgets, 1)
	assert.Equal(t, 50.0, budgets[0].Amount)
}

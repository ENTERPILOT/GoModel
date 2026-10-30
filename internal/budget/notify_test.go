package budget

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceAnnouncesEveryWrite(t *testing.T) {
	ctx := context.Background()
	service, err := NewService(ctx, &fakeStore{})
	require.NoError(t, err)
	var announced int
	service.SetChangeNotifier(func(context.Context) { announced++ })

	item := Budget{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: 86400, Amount: 5}
	require.NoError(t, service.UpsertBudgets(ctx, []Budget{item}))
	require.NoError(t, service.ReplaceConfigBudgets(ctx, []Budget{item}))
	_, err = service.SaveSettings(ctx, DefaultSettings())
	require.NoError(t, err)
	require.NoError(t, service.ResetBudget(ctx, ScopeUserPath, "/team", 86400, time.Now()))
	require.NoError(t, service.ResetAll(ctx, time.Now()))
	require.NoError(t, service.DeleteBudget(ctx, ScopeUserPath, "/team", 86400))
	assert.Equal(t, 6, announced)

	// A write the service refuses changed nothing.
	err = service.DeleteBudget(ctx, ScopeUserPath, "/team", 0)
	require.Error(t, err)
	assert.Equal(t, 6, announced)
}

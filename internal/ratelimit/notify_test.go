package ratelimit

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceAnnouncesRuleWrites(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	var announced int
	service.SetChangeNotifier(func(context.Context) { announced++ })

	limit := int64(5)
	rule := Rule{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: 60, MaxRequests: &limit}
	require.NoError(t, service.UpsertRules(ctx, []Rule{rule}))
	require.NoError(t, service.ReplaceConfigRules(ctx, []Rule{rule}))
	require.NoError(t, service.DeleteRule(ctx, ScopeUserPath, "/team", 60))
	assert.Equal(t, 3, announced)

	// Counter resets live in this instance's memory: nothing is stored.
	require.NoError(t, service.ResetAll())
	assert.Equal(t, 3, announced)
}

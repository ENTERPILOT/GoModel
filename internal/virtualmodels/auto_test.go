package virtualmodels

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// upsertAuto stores the auto route "auto" over the balancing catalog:
// groq/llama is light, openai/gpt-4o and local/mistral are smart, and
// anthropic/claude is smartest.
func upsertAuto(t *testing.T, svc *Service) {
	t.Helper()
	err := svc.Upsert(context.Background(), VirtualModel{
		Source:   "auto",
		Strategy: StrategyAuto,
		Targets: []Target{
			{Model: "anthropic/claude", Level: core.RouteLevelSmartest},
			{Model: "groq/llama", Level: core.RouteLevelLight},
			{Model: "openai/gpt-4o", Level: core.RouteLevelSmart},
			{Model: "local/mistral", Level: core.RouteLevelSmart},
		},
		Enabled: true,
	})
	require.NoError(t, err)
}

// resolveLevel resolves source for a request that needs level ("" for a
// request nothing classified) in session sessionID.
func resolveLevel(t *testing.T, svc *Service, source, level, sessionID string) string {
	t.Helper()
	ctx := core.WithRouteLevel(context.Background(), level)
	resolution, _, err := svc.resolveRequested(ctx, core.NewRequestedModelSelector(source, ""), "", false, sessionID)
	require.NoError(t, err)
	return resolution.Resolved.QualifiedModel()
}

func TestAuto_RoutesToLowestLevelCoveringTheRequest(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertAuto(t, svc)

	tests := []struct {
		name  string
		level string
		want  []string
	}{
		{name: "light request takes the light target", level: core.RouteLevelLight, want: []string{"groq/llama"}},
		{name: "a level without targets moves up", level: core.RouteLevelNormal, want: []string{"openai/gpt-4o", "local/mistral"}},
		{name: "smart request balances the smart pool", level: core.RouteLevelSmart, want: []string{"openai/gpt-4o", "local/mistral"}},
		{name: "smartest request takes the top target", level: core.RouteLevelSmartest, want: []string{"anthropic/claude"}},
		{name: "unclassified request takes the top target", level: "", want: []string{"anthropic/claude"}},
		{name: "unknown level counts as unclassified", level: "genius", want: []string{"anthropic/claude"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seen := map[string]bool{}
			for range 4 {
				seen[resolveLevel(t, svc, "auto", tt.level, "")] = true
			}
			got := make([]string, 0, len(seen))
			for model := range seen {
				got = append(got, model)
			}
			assert.ElementsMatch(t, tt.want, got)
		})
	}
}

func TestAuto_ServesLowerLevelWhenNothingCoversTheRequest(t *testing.T) {
	t.Parallel()
	catalog := balancingCatalog()
	catalog.stale = map[string]bool{"anthropic/claude": true}
	svc, err := NewService(newSQLVMStore(t), catalog, true)
	require.NoError(t, err)
	upsertAuto(t, svc)

	// The only smartest target is unavailable, so the highest level left
	// serves, for a classified request and an unclassified one alike.
	for _, level := range []string{core.RouteLevelSmartest, ""} {
		got := resolveLevel(t, svc, "auto", level, "")
		assert.Contains(t, []string{"openai/gpt-4o", "local/mistral"}, got, "level %q", level)
	}
}

func TestAuto_SkipsLevelWithoutRateLimitCapacity(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	svc.SetTargetCapacity(func(qualified string) bool { return qualified != "groq/llama" })
	upsertAuto(t, svc)

	got := resolveLevel(t, svc, "auto", core.RouteLevelLight, "")
	assert.Contains(t, []string{"openai/gpt-4o", "local/mistral"}, got, "a saturated level hands the request to the next one up")
}

func TestAuto_SaturatedRouteReachesAdmissionAtTheRequestedLevel(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	svc.SetTargetCapacity(func(string) bool { return false })
	upsertAuto(t, svc)

	// Every target is saturated: the request still resolves, to a target of
	// its own level, so admission answers with that model's 429.
	assert.Equal(t, "groq/llama", resolveLevel(t, svc, "auto", core.RouteLevelLight, ""))
	assert.Equal(t, "anthropic/claude", resolveLevel(t, svc, "auto", "", ""))
}

func TestAuto_SessionKeepsOneTargetPerLevel(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertAuto(t, svc)

	smart := resolveLevel(t, svc, "auto", core.RouteLevelSmart, "sess-a")
	for range 4 {
		require.Equal(t, smart, resolveLevel(t, svc, "auto", core.RouteLevelSmart, "sess-a"), "a session stays on its target within a level")
	}
	// The conversation gets harder, then easier again: it moves level both
	// ways and comes back to the smart target it used before.
	require.Equal(t, "anthropic/claude", resolveLevel(t, svc, "auto", core.RouteLevelSmartest, "sess-a"))
	require.Equal(t, smart, resolveLevel(t, svc, "auto", core.RouteLevelSmart, "sess-a"))
	require.Equal(t, "groq/llama", resolveLevel(t, svc, "auto", core.RouteLevelLight, "sess-a"))
}

func TestAuto_SelectorBalancesInsideTheLevel(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	selector := &scriptedSelector{answer: "local/mistral"}
	svc.SetRouteSelector(selector)
	upsertAuto(t, svc)

	require.Equal(t, "local/mistral", resolveLevel(t, svc, "auto", core.RouteLevelSmart, ""))
	requests := selector.seen()
	require.Len(t, requests, 1)
	candidates := make([]string, len(requests[0].Candidates))
	for i, candidate := range requests[0].Candidates {
		candidates[i] = candidate.Qualified
	}
	assert.Equal(t, []string{"openai/gpt-4o", "local/mistral"}, candidates, "the selector sees only the chosen level")

	// A level with one target needs no selector.
	require.Equal(t, "groq/llama", resolveLevel(t, svc, "auto", core.RouteLevelLight, ""))
	assert.Len(t, selector.seen(), 1)
}

func TestAuto_FailoverStaysAtOrAboveTheServedLevelFirst(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertAuto(t, svc)

	assert.Equal(t, []string{"local/mistral", "anthropic/claude", "groq/llama"}, failoverChainAt(t, svc, "auto", "openai/gpt-4o", core.RouteLevelSmart),
		"same level, then higher, then lower")
	assert.Equal(t, []string{"openai/gpt-4o", "local/mistral", "anthropic/claude"}, failoverChainAt(t, svc, "auto", "groq/llama", core.RouteLevelLight),
		"a light request climbs level by level")
	assert.Equal(t, []string{"openai/gpt-4o", "local/mistral", "groq/llama"}, failoverChainAt(t, svc, "auto", "anthropic/claude", core.RouteLevelSmartest),
		"the top level falls to the nearest level below first")
	assert.Equal(t, []string{"openai/gpt-4o", "local/mistral", "groq/llama"}, failoverChainAt(t, svc, "auto", "anthropic/claude", ""),
		"a resolution without a level counts as the highest one")
}

// failoverChainAt returns the failover legs of a request for source that an
// auto route served from the model served at level.
func failoverChainAt(t *testing.T, svc *Service, source, served, level string) []string {
	t.Helper()
	selector, err := core.ParseModelSelector(served, "")
	require.NoError(t, err)
	resolution := &core.RequestModelResolution{
		Requested:        core.NewRequestedModelSelector(source, ""),
		ResolvedSelector: selector,
		AliasApplied:     true,
		RouteLevel:       level,
	}
	var chain []string
	for _, leg := range svc.ResolveFailovers(resolution, core.OperationChatCompletions) {
		chain = append(chain, leg.QualifiedModel())
	}
	return chain
}

func TestAuto_FailoverOrderAppliesBehindAnAlias(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertAuto(t, svc)
	upsertRedirect(t, svc, "default", "", "auto")

	chain := failoverChainAt(t, svc, "default", "groq/llama", core.RouteLevelLight)
	assert.Equal(t, []string{"openai/gpt-4o", "local/mistral", "anthropic/claude"}, chain)
}

func TestAuto_ValidatesLevels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		targets []Target
		wantErr string
	}{
		{
			name:    "level is required",
			targets: []Target{{Model: "groq/llama", Level: "light"}, {Model: "openai/gpt-4o"}},
			wantErr: `target "openai/gpt-4o" needs a level`,
		},
		{
			name:    "level must be known",
			targets: []Target{{Model: "groq/llama", Level: "genius"}},
			wantErr: `unknown level "genius"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newBalancingService(t)
			err := svc.Upsert(context.Background(), VirtualModel{Source: "auto", Strategy: StrategyAuto, Targets: tt.targets, Enabled: true})
			require.Error(t, err)
			assert.True(t, IsValidationError(err))
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestAuto_NormalizesLevelsAndDropsThemUnderOtherStrategies(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	ctx := context.Background()
	targets := []Target{{Model: "groq/llama", Level: " Light "}, {Model: "openai/gpt-4o", Level: "SMARTEST"}}

	require.NoError(t, svc.Upsert(ctx, VirtualModel{Source: "auto", Strategy: StrategyAuto, Targets: targets, Enabled: true}))
	stored, ok := svc.Get("auto")
	require.True(t, ok)
	assert.Equal(t, "light", stored.Targets[0].Level)
	assert.Equal(t, "smartest", stored.Targets[1].Level)

	require.NoError(t, svc.Upsert(ctx, VirtualModel{Source: "auto", Strategy: StrategyRoundRobin, Targets: targets, Enabled: true}))
	stored, ok = svc.Get("auto")
	require.True(t, ok)
	assert.Empty(t, stored.Targets[0].Level)
	assert.Empty(t, stored.Targets[1].Level)
}

func TestAuto_RejectsAutoRouteBehindAutoRoute(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	ctx := context.Background()
	upsertAuto(t, svc)
	upsertRedirect(t, svc, "alias", "", "auto")

	err := svc.Upsert(ctx, VirtualModel{
		Source:   "outer",
		Strategy: StrategyAuto,
		Targets:  []Target{{Model: "groq/llama", Level: "light"}, {Model: "alias", Level: "smartest"}},
		Enabled:  true,
	})
	require.Error(t, err)
	assert.True(t, IsValidationError(err))
	assert.Contains(t, err.Error(), `routes into "auto"`)
}

func TestAuto_AutoRouteLookup(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	ctx := context.Background()
	upsertAuto(t, svc)
	upsertRedirect(t, svc, "default", "", "auto")
	upsertRedirect(t, svc, "mixed", StrategyRoundRobin, "auto", "groq/llama")
	upsertRedirect(t, svc, "plain", "", "groq/llama")
	require.NoError(t, svc.Upsert(ctx, VirtualModel{
		Source:    "team-auto",
		Strategy:  StrategyAuto,
		Targets:   []Target{{Model: "groq/llama", Level: "normal"}, {Model: "anthropic/claude", Level: "smartest"}},
		UserPaths: []string{"/team-a"},
		Enabled:   true,
	}))

	want := ext.AutoRoute{Source: "auto", Levels: []string{"light", "smart", "smartest"}}
	route, ok := svc.AutoRoute("auto", "")
	require.True(t, ok)
	assert.Equal(t, want, route)

	route, ok = svc.AutoRoute("default", "")
	require.True(t, ok, "a single-target alias of an auto route is followed")
	assert.Equal(t, want, route)

	for _, model := range []string{"mixed", "plain", "groq/llama", "missing", ""} {
		_, ok = svc.AutoRoute(model, "")
		assert.False(t, ok, "model %q is not an auto route", model)
	}

	route, ok = svc.AutoRoute("team-auto", "/team-a/alice")
	require.True(t, ok)
	assert.Equal(t, []string{"normal", "smartest"}, route.Levels)
	_, ok = svc.AutoRoute("team-auto", "/team-b")
	assert.False(t, ok, "a route scoped to other user paths is not reported")
}

// servedLevel resolves source for a request that needs level and returns
// the model and the level the route reports it chose.
func servedLevel(t *testing.T, svc *Service, source, level string) (model, served string) {
	t.Helper()
	ctx, report := core.WithServedRouteLevel(core.WithRouteLevel(context.Background(), level))
	resolution, _, err := svc.resolveRequested(ctx, core.NewRequestedModelSelector(source, ""), "", false, "")
	require.NoError(t, err)
	return resolution.Resolved.QualifiedModel(), report.Level
}

func TestAuto_ReportsTheLevelItChose(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertAuto(t, svc)
	upsertRedirect(t, svc, "default", "", "auto")
	upsertRedirect(t, svc, "plain", "", "groq/llama")

	for _, tc := range []struct {
		source, needs, want string
	}{
		{"auto", core.RouteLevelLight, core.RouteLevelLight},
		{"auto", core.RouteLevelNormal, core.RouteLevelSmart},
		{"auto", "", core.RouteLevelSmartest},
		{"default", core.RouteLevelLight, core.RouteLevelLight},
		{"plain", core.RouteLevelLight, ""},
		{"groq/llama", core.RouteLevelLight, ""},
	} {
		_, served := servedLevel(t, svc, tc.source, tc.needs)
		assert.Equal(t, tc.want, served, "source %q needing %q", tc.source, tc.needs)
	}
}

// One model can sit behind targets of two levels when those targets are
// virtual models. The level reported, and the failover order, must follow
// the target that was chosen, not the first target the model appears under.
func TestAuto_LevelFollowsTheChosenTargetWhenLevelsShareAModel(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertRedirect(t, svc, "pool-light", StrategyFailover, "openai/gpt-4o", "groq/llama")
	upsertRedirect(t, svc, "pool-top", StrategyFailover, "openai/gpt-4o", "anthropic/claude")
	require.NoError(t, svc.Upsert(context.Background(), VirtualModel{
		Source:   "auto",
		Strategy: StrategyAuto,
		Targets:  []Target{{Model: "pool-light", Level: core.RouteLevelLight}, {Model: "pool-top", Level: core.RouteLevelSmartest}},
		Enabled:  true,
	}))

	model, served := servedLevel(t, svc, "auto", core.RouteLevelSmartest)
	require.Equal(t, "openai/gpt-4o", model)
	require.Equal(t, core.RouteLevelSmartest, served)
	assert.Equal(t, []string{"anthropic/claude", "groq/llama"}, failoverChainAt(t, svc, "auto", model, served),
		"the rest of the smartest level comes before the light one")
}

// Each level rotates on its own: traffic at one level must not shift which
// target the next request of another level gets.
func TestAuto_EachLevelKeepsItsOwnRotation(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	require.NoError(t, svc.Upsert(context.Background(), VirtualModel{
		Source:   "auto",
		Strategy: StrategyAuto,
		Targets: []Target{
			{Model: "groq/llama", Level: core.RouteLevelLight},
			{Model: "anthropic/claude", Level: core.RouteLevelLight},
			{Model: "openai/gpt-4o", Level: core.RouteLevelSmart},
			{Model: "local/mistral", Level: core.RouteLevelSmart},
		},
		Enabled: true,
	}))

	counts := map[string]int{}
	for range 20 {
		counts[resolveLevel(t, svc, "auto", core.RouteLevelLight, "")]++
		counts[resolveLevel(t, svc, "auto", core.RouteLevelSmart, "")]++
	}
	assert.Equal(t, map[string]int{"groq/llama": 10, "anthropic/claude": 10, "openai/gpt-4o": 10, "local/mistral": 10}, counts)
}

// With a route selector installed (GoModel Pro's adaptive selector), a
// session still keeps one pin per level, and the selector is shown the pin
// of the level it is choosing in.
func TestAuto_SelectorSeesTheSessionPinOfItsLevel(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	selector := &scriptedSelector{answer: "local/mistral"}
	svc.SetRouteSelector(selector)
	upsertAuto(t, svc)

	require.Equal(t, "local/mistral", resolveLevel(t, svc, "auto", core.RouteLevelSmart, "sess-a"))
	// The session visits another level in between; its smart pin survives.
	require.Equal(t, "anthropic/claude", resolveLevel(t, svc, "auto", core.RouteLevelSmartest, "sess-a"))
	require.Equal(t, "local/mistral", resolveLevel(t, svc, "auto", core.RouteLevelSmart, "sess-a"))

	requests := selector.seen()
	require.Len(t, requests, 2, "the one-target smartest level needs no selector")
	assert.Empty(t, requests[0].SessionTarget, "the first smart request has no pin yet")
	assert.Equal(t, "local/mistral", requests[1].SessionTarget, "the second is shown the session's smart pin")
	assert.Equal(t, "sess-a", requests[1].SessionID, "the selector gets the session id, not the per-level pin key")
}

// A level with a single target has nothing to keep a session on, so it must
// not take a slot in the shared pin table.
func TestAuto_SingleTargetLevelsStoreNoPins(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	require.NoError(t, svc.Upsert(context.Background(), VirtualModel{
		Source:   "auto",
		Strategy: StrategyAuto,
		Targets:  []Target{{Model: "groq/llama", Level: core.RouteLevelLight}, {Model: "anthropic/claude", Level: core.RouteLevelSmartest}},
		Enabled:  true,
	}))
	for _, level := range []string{core.RouteLevelLight, core.RouteLevelSmartest, ""} {
		resolveLevel(t, svc, "auto", level, "sess-a")
	}
	assert.Empty(t, svc.sticky.entries)
}

func TestLevelPool(t *testing.T) {
	t.Parallel()
	targets := []resolvedTarget{
		{qualified: "a", level: 4},
		{qualified: "b", level: 1},
		{qualified: "c", level: 3},
		{qualified: "d", level: 3},
	}
	names := func(pool []resolvedTarget) []string {
		out := make([]string, len(pool))
		for i, target := range pool {
			out[i] = target.qualified
		}
		return out
	}
	assert.Equal(t, []string{"a"}, names(levelPool(targets, 0)))
	assert.Equal(t, []string{"b"}, names(levelPool(targets, 1)))
	assert.Equal(t, []string{"c", "d"}, names(levelPool(targets, 2)))
	assert.Equal(t, []string{"a"}, names(levelPool(targets, 4)))
	assert.Equal(t, []string{"c", "d"}, names(levelPool(targets[1:], 4)), "nothing covers the request: the highest level present serves")
	assert.Empty(t, levelPool(nil, 2))
}

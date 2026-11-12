package users

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/requestparams"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

func TestService_RequestPolicyInheritsDownTheTree(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()
	for _, user := range []User{
		{UserPath: "/", MaxOutputTokens: 8000},
		{UserPath: "/acme", DefaultModel: "openai/gpt-4o", MaxReasoningEffort: "high"},
		{UserPath: "/acme/eng", DefaultModel: "team-model", MaxReasoningEffort: "medium", MaxOutputTokens: 4096},
		// A child asking for more than its group allows still gets the
		// group's ceilings: only its default model takes effect.
		{UserPath: "/acme/eng/bob", MaxReasoningEffort: "max", MaxOutputTokens: 100000},
		{UserPath: "/acme/eng/carol", DefaultModel: "anthropic/claude-sonnet-4-6", MaxReasoningEffort: "low", MaxOutputTokens: 512},
		// An allowlist-only row sets no request policy of its own.
		{UserPath: "/acme/sales", AllowedModels: []string{"openai/*"}},
	} {
		_, err := svc.Upsert(ctx, user)
		require.NoError(t, err, user.UserPath)
	}

	tests := []struct {
		name     string
		userPath string
		want     RequestPolicy
	}{
		{"no user path has no policy", "", RequestPolicy{}},
		{"unrelated path inherits the root ceiling only", "/other/dave", RequestPolicy{
			MaxOutputTokens: 8000, MaxOutputTokensFrom: "/",
		}},
		{"group sets a default and an effort ceiling", "/acme", RequestPolicy{
			DefaultModel: "openai/gpt-4o", DefaultModelFrom: "/acme",
			MaxReasoningEffort: "high", MaxReasoningEffortFrom: "/acme",
			MaxOutputTokens: 8000, MaxOutputTokensFrom: "/",
		}},
		{"row without request policy inherits its group's", "/acme/sales/erin", RequestPolicy{
			DefaultModel: "openai/gpt-4o", DefaultModelFrom: "/acme",
			MaxReasoningEffort: "high", MaxReasoningEffortFrom: "/acme",
			MaxOutputTokens: 8000, MaxOutputTokensFrom: "/",
		}},
		{"nearest default wins and the tightest ceilings win", "/acme/eng/alice", RequestPolicy{
			DefaultModel: "team-model", DefaultModelFrom: "/acme/eng",
			MaxReasoningEffort: "medium", MaxReasoningEffortFrom: "/acme/eng",
			MaxOutputTokens: 4096, MaxOutputTokensFrom: "/acme/eng",
		}},
		{"child cannot loosen an ancestor's ceilings", "/acme/eng/bob", RequestPolicy{
			DefaultModel: "team-model", DefaultModelFrom: "/acme/eng",
			MaxReasoningEffort: "medium", MaxReasoningEffortFrom: "/acme/eng",
			MaxOutputTokens: 4096, MaxOutputTokensFrom: "/acme/eng",
		}},
		{"child tightens ceilings and replaces the default", "/acme/eng/carol/laptop", RequestPolicy{
			DefaultModel: "anthropic/claude-sonnet-4-6", DefaultModelFrom: "/acme/eng/carol",
			MaxReasoningEffort: "low", MaxReasoningEffortFrom: "/acme/eng/carol",
			MaxOutputTokens: 512, MaxOutputTokensFrom: "/acme/eng/carol",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := svc.RequestPolicy(tt.userPath)
			assert.Equal(t, tt.want, got)

			ctx := requestCtx(tt.userPath)
			assert.Equal(t, tt.want.DefaultModel, svc.DefaultModel(ctx))
			assert.Equal(t, requestparams.Ceilings{ReasoningEffort: tt.want.MaxReasoningEffort, OutputTokens: tt.want.MaxOutputTokens}, svc.RequestCeilings(ctx))
		})
	}
}

func TestService_RequestPolicyClearsWithTheRow(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()
	assert.True(t, svc.RequestPolicy("/acme/eng").IsZero(), "nothing is configured by default")

	_, err := svc.Upsert(ctx, User{UserPath: "/acme", DefaultModel: "openai/gpt-4o", MaxReasoningEffort: "low", MaxOutputTokens: 100})
	require.NoError(t, err)
	assert.False(t, svc.RequestPolicy("/acme/eng").IsZero())

	// A write that leaves the fields out clears them, like the allowlist.
	_, err = svc.Upsert(ctx, User{UserPath: "/acme", AllowedModels: []string{"openai/*"}})
	require.NoError(t, err)
	assert.True(t, svc.RequestPolicy("/acme/eng").IsZero())

	_, err = svc.Upsert(ctx, User{UserPath: "/acme", MaxOutputTokens: 100})
	require.NoError(t, err)
	require.NoError(t, svc.Delete(ctx, "/acme"))
	assert.True(t, svc.RequestPolicy("/acme/eng").IsZero())
}

func TestService_UpsertNormalizesAndValidatesRequestPolicy(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	stored, err := svc.Upsert(ctx, User{UserPath: "/acme", DefaultModel: "  team-model ", MaxReasoningEffort: " HIGH ", MaxOutputTokens: 2048})
	require.NoError(t, err)
	assert.Equal(t, "team-model", stored.DefaultModel)
	assert.Equal(t, "high", stored.MaxReasoningEffort)
	assert.Equal(t, 2048, stored.MaxOutputTokens)

	invalid := []struct {
		name string
		user User
		want string
	}{
		{"effort outside the common scale", User{UserPath: "/acme", MaxReasoningEffort: "extreme"}, "unknown max_reasoning_effort"},
		{"negative token ceiling", User{UserPath: "/acme", MaxOutputTokens: -1}, "max_output_tokens must not be negative"},
		{"default model that is a provider wildcard", User{UserPath: "/acme", DefaultModel: "openai/*"}, "invalid default_model"},
		{"default model that is a whole provider", User{UserPath: "/acme", DefaultModel: "openai/"}, "invalid default_model"},
		{"default model with whitespace", User{UserPath: "/acme", DefaultModel: "gpt 4o"}, "invalid default_model"},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.Upsert(ctx, tt.user)
			require.True(t, IsValidationError(err), "err = %v", err)
			assert.ErrorContains(t, err, tt.want)
		})
	}
	kept, ok := svc.Get("/acme")
	require.True(t, ok)
	assert.Equal(t, "high", kept.MaxReasoningEffort, "a rejected write must not change the stored policy")
}

func TestService_ConfigUsersCarryRequestPolicy(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()
	_, err := svc.Upsert(ctx, User{UserPath: "/acme", MaxReasoningEffort: "max", MaxOutputTokens: 9000})
	require.NoError(t, err)

	svc.SetConfigUsers([]User{{UserPath: "acme", DefaultModel: " team-model ", MaxReasoningEffort: "Medium", MaxOutputTokens: 1000}})
	require.NoError(t, svc.ValidateManagedConfig([]string{"openai"}))
	require.NoError(t, svc.Refresh(ctx))

	assert.Equal(t, RequestPolicy{
		DefaultModel: "team-model", DefaultModelFrom: "/acme",
		MaxReasoningEffort: "medium", MaxReasoningEffortFrom: "/acme",
		MaxOutputTokens: 1000, MaxOutputTokensFrom: "/acme",
	}, svc.RequestPolicy("/acme/eng"), "the declared row shadows the stored one")

	_, err = svc.Upsert(ctx, User{UserPath: "/acme", MaxOutputTokens: 5})
	require.ErrorIs(t, err, ErrManaged)

	svc.SetConfigUsers([]User{{UserPath: "/acme", MaxReasoningEffort: "extreme"}})
	require.ErrorContains(t, svc.ValidateManagedConfig([]string{"openai"}), "unknown max_reasoning_effort")
	svc.SetConfigUsers([]User{{UserPath: "/acme", MaxOutputTokens: -5}})
	require.ErrorContains(t, svc.ValidateManagedConfig([]string{"openai"}), "max_output_tokens")
}

// A second gateway instance sees a policy write once it reloads, which is
// what the cluster-sync notification and the background refresh trigger.
func TestService_RefreshPicksUpRequestPolicyWrittenElsewhere(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := NewSQLStore(ctx, sqlxtest.NewSQLite(t))
	require.NoError(t, err)
	writer, err := NewService(store, testCatalog{"openai"})
	require.NoError(t, err)
	reader, err := NewService(store, testCatalog{"openai"})
	require.NoError(t, err)
	require.NoError(t, reader.Refresh(ctx))

	notified := 0
	writer.SetChangeNotifier(func(context.Context) { notified++ })
	_, err = writer.Upsert(ctx, User{UserPath: "/acme", MaxReasoningEffort: "low"})
	require.NoError(t, err)

	assert.Equal(t, 1, notified)
	assert.True(t, reader.RequestPolicy("/acme").IsZero(), "the reader has not reloaded yet")
	require.NoError(t, reader.Refresh(ctx))
	assert.Equal(t, "low", reader.RequestPolicy("/acme/eng").MaxReasoningEffort)
}

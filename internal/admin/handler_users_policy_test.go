package admin

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/authkeys"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/users"
)

func TestUsersTreeReportsRequestPolicy(t *testing.T) {
	now := time.Now().UTC()
	h := newUsersHandler(t,
		authkeys.AuthKey{ID: "k1", Name: "alice", UserPath: "/acme/eng/alice", SecretHash: "h1", Enabled: true, CreatedAt: now, UpdatedAt: now},
		authkeys.AuthKey{ID: "k2", Name: "ops", UserPath: "/other/ops", SecretHash: "h2", Enabled: true, CreatedAt: now, UpdatedAt: now},
	)

	for _, body := range []string{
		`{"user_path":"/acme","default_model":"openai/gpt-4o","max_reasoning_effort":"High","max_output_tokens":8000}`,
		`{"user_path":"/acme/eng","default_model":" team-model ","max_reasoning_effort":"medium"}`,
		`{"user_path":"/acme/eng/bob","max_reasoning_effort":"max","max_output_tokens":100000}`,
	} {
		c, rec := echotest.Request(t, http.MethodPut, "/admin/users", body)
		require.NoError(t, h.UpsertUser(c))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}

	c, rec := echotest.Get(t, "/admin/users")
	require.NoError(t, h.ListUsers(c))
	nodes := decodeUsers(t, rec)

	acme := nodes["/acme"]
	assert.Equal(t, "openai/gpt-4o", acme.DefaultModel)
	assert.Equal(t, "high", acme.MaxReasoningEffort)
	assert.Equal(t, 8000, acme.MaxOutputTokens)
	assert.Empty(t, acme.InheritedFrom, "request policy does not count as a model restriction")
	assert.False(t, acme.Restricted)

	eng := nodes["/acme/eng"]
	assert.Equal(t, "team-model", eng.DefaultModel)
	assert.Equal(t, "medium", eng.MaxReasoningEffort)
	assert.Zero(t, eng.MaxOutputTokens, "the node's own fields list only what it sets itself")

	inherited := &users.RequestPolicy{
		DefaultModel: "team-model", DefaultModelFrom: "/acme/eng",
		MaxReasoningEffort: "medium", MaxReasoningEffortFrom: "/acme/eng",
		MaxOutputTokens: 8000, MaxOutputTokensFrom: "/acme",
	}
	assert.Equal(t, inherited, eng.RequestPolicy)

	alice := nodes["/acme/eng/alice"]
	assert.False(t, alice.Configured)
	assert.Empty(t, alice.MaxReasoningEffort)
	assert.Equal(t, inherited, alice.RequestPolicy, "an unconfigured node inherits everything")

	bob := nodes["/acme/eng/bob"]
	assert.Equal(t, "max", bob.MaxReasoningEffort, "the node keeps what was saved")
	assert.Equal(t, inherited, bob.RequestPolicy, "but a child cannot loosen its group's ceilings")

	assert.Nil(t, nodes["/other/ops"].RequestPolicy)
	assert.Nil(t, nodes["/"].RequestPolicy)

	// A PUT replaces the whole policy: fields left out are cleared.
	c, rec = echotest.Request(t, http.MethodPut, "/admin/users", `{"user_path":"/acme/eng","allowed_models":["openai/*"]}`)
	require.NoError(t, h.UpsertUser(c))
	nodes = decodeUsers(t, rec)
	assert.Empty(t, nodes["/acme/eng"].DefaultModel)
	assert.Equal(t, &users.RequestPolicy{
		DefaultModel: "openai/gpt-4o", DefaultModelFrom: "/acme",
		MaxReasoningEffort: "high", MaxReasoningEffortFrom: "/acme",
		MaxOutputTokens: 8000, MaxOutputTokensFrom: "/acme",
	}, nodes["/acme/eng/alice"].RequestPolicy)
}

func TestUpsertUserValidatesRequestPolicy(t *testing.T) {
	h := newUsersHandler(t)
	for name, body := range map[string]string{
		"unknown effort":         `{"user_path":"/acme","max_reasoning_effort":"extreme"}`,
		"negative token ceiling": `{"user_path":"/acme","max_output_tokens":-1}`,
		"wildcard default model": `{"user_path":"/acme","default_model":"openai/*"}`,
		"non-integer ceiling":    `{"user_path":"/acme","max_output_tokens":"many"}`,
	} {
		t.Run(name, func(t *testing.T) {
			c, rec := echotest.Request(t, http.MethodPut, "/admin/users", body)
			require.NoError(t, h.UpsertUser(c))
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}
	c, rec := echotest.Get(t, "/admin/users")
	require.NoError(t, h.ListUsers(c))
	assert.Empty(t, decodeUsers(t, rec), "a rejected write stores nothing")
}

func TestUpsertUserKeepsManagedRequestPolicyReadOnly(t *testing.T) {
	ctx := context.Background()
	h := newUsersHandler(t)
	h.users.SetConfigUsers([]users.User{{UserPath: "/acme", DefaultModel: "team-model", MaxReasoningEffort: "medium", MaxOutputTokens: 4096}})
	require.NoError(t, h.users.ValidateManagedConfig([]string{"openai"}))
	require.NoError(t, h.users.Refresh(ctx))

	c, rec := echotest.Request(t, http.MethodPut, "/admin/users", `{"user_path":"/acme","max_reasoning_effort":"max"}`)
	require.NoError(t, h.UpsertUser(c))
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, "user_managed", errorCode(t, rec))

	c, rec = echotest.Request(t, http.MethodDelete, "/admin/users?user_path=/acme", nil)
	require.NoError(t, h.DeleteUser(c))
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())

	c, rec = echotest.Get(t, "/admin/users")
	require.NoError(t, h.ListUsers(c))
	acme := decodeUsers(t, rec)["/acme"]
	assert.True(t, acme.Managed)
	assert.Equal(t, "medium", acme.MaxReasoningEffort)
	assert.Equal(t, 4096, acme.MaxOutputTokens)
	assert.Equal(t, "team-model", acme.DefaultModel)
}

// A scoped credential was granted the policy on its scope root, so it cannot
// rewrite it; below the root it may set policy, which can tighten the
// granted ceilings but never loosen them.
func TestUsersScopedRequestPolicy(t *testing.T) {
	h := newUsersHandler(t)
	c, rec := scopedRequest(t, http.MethodPut, "/admin/users", `{"user_path":"/team/alpha","default_model":"openai/gpt-4o","max_reasoning_effort":"medium","max_output_tokens":4096}`, "")
	require.NoError(t, h.UpsertUser(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	for name, body := range map[string]string{
		"loosen the root ceilings":   `{"user_path":"/team/alpha","max_reasoning_effort":"max","max_output_tokens":100000}`,
		"clear the root policy":      `{"user_path":"/team/alpha"}`,
		"change the root default":    `{"user_path":"/team/alpha","default_model":"openai/o3","max_reasoning_effort":"medium","max_output_tokens":4096}`,
		"write a sibling's policy":   `{"user_path":"/team/beta","max_output_tokens":10}`,
		"write an ancestor's policy": `{"user_path":"/team","max_reasoning_effort":"max"}`,
	} {
		t.Run(name, func(t *testing.T) {
			c, rec := scopedRequest(t, http.MethodPut, "/admin/users", body, scopeAlpha)
			require.NoError(t, h.UpsertUser(c))
			assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			assert.Equal(t, codeUserPathOutOfScope, errorCode(t, rec))
		})
	}
	c, rec = scopedRequest(t, http.MethodDelete, "/admin/users?user_path=/team/alpha", "", scopeAlpha)
	require.NoError(t, h.DeleteUser(c))
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

	c, rec = scopedRequest(t, http.MethodPut, "/admin/users", `{"user_path":"/team/alpha/svc","default_model":"openai/gpt-4o-mini","max_reasoning_effort":"max","max_output_tokens":1024}`, scopeAlpha)
	require.NoError(t, h.UpsertUser(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	nodes := decodeUsers(t, rec)

	root := nodes["/team/alpha"]
	assert.Equal(t, "medium", root.MaxReasoningEffort, "the scope root keeps the policy it was granted")
	assert.Equal(t, 4096, root.MaxOutputTokens)
	assert.Equal(t, &users.RequestPolicy{
		DefaultModel: "openai/gpt-4o-mini", DefaultModelFrom: "/team/alpha/svc",
		MaxReasoningEffort: "medium", MaxReasoningEffortFrom: "/team/alpha",
		MaxOutputTokens: 1024, MaxOutputTokensFrom: "/team/alpha/svc",
	}, nodes["/team/alpha/svc"].RequestPolicy)
}

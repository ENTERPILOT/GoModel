package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyUsersEnv_ParsesAndMergesByPath(t *testing.T) {
	cfg := &Config{Users: []UserConfig{
		{Path: "/acme", AllowedModels: []string{"openai/*"}},
		{Path: "/acme/eng", AllowedModels: []string{"anthropic/*"}},
	}}
	t.Setenv(envUsers, `[
		{"path":"/ACME","allowed_models":["anthropic/*","openai/gpt-4o"],"description":"root"},
		{"path":"/acme/sales","allowed_models":["openai/gpt-4o-mini"]}
	]`)
	err := applyUsersEnv(cfg, true)
	require.NoError(t, err)
	require.Len(t, cfg.Users, 3)

	root := cfg.Users[0]
	require.Equal(t, "root", root.Description)
	require.Equal(t, []string{"anthropic/*", "openai/gpt-4o"}, root.AllowedModels, "env did not override /acme: %#v", root)
	require.Equal(t, "/acme/eng", cfg.Users[1].Path)
	require.Equal(t, "/acme/sales", cfg.Users[2].Path, "merge order wrong: %#v", cfg.Users)
}

func TestApplyUsersEnv_Invalid(t *testing.T) {
	cfg := &Config{}
	t.Setenv(envUsers, `{not valid json`)
	require.Error(t, applyUsersEnv(cfg, true))

	t.Setenv(envUsers, `[{"path":"/acme","allowed_modles":["openai/*"]}]`)
	require.Error(t, applyUsersEnv(cfg, true))
	err := applyUsersEnv(cfg, false)
	require.NoError(t, err)
}

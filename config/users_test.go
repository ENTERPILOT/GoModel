package config

import (
	"reflect"
	"testing"
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

	if err := applyUsersEnv(cfg, true); err != nil {
		t.Fatalf("applyUsersEnv() error = %v", err)
	}
	if len(cfg.Users) != 3 {
		t.Fatalf("merged len = %d, want 3: %#v", len(cfg.Users), cfg.Users)
	}
	root := cfg.Users[0]
	if root.Description != "root" || !reflect.DeepEqual(root.AllowedModels, []string{"anthropic/*", "openai/gpt-4o"}) {
		t.Fatalf("env did not override /acme: %#v", root)
	}
	if cfg.Users[1].Path != "/acme/eng" || cfg.Users[2].Path != "/acme/sales" {
		t.Fatalf("merge order wrong: %#v", cfg.Users)
	}
}

func TestApplyUsersEnv_Invalid(t *testing.T) {
	cfg := &Config{}
	t.Setenv(envUsers, `{not valid json`)
	if err := applyUsersEnv(cfg, true); err == nil {
		t.Fatal("applyUsersEnv() error = nil, want parse error")
	}
	t.Setenv(envUsers, `[{"path":"/acme","allowed_modles":["openai/*"]}]`)
	if err := applyUsersEnv(cfg, true); err == nil {
		t.Fatal("applyUsersEnv() strict error = nil, want unknown field error")
	}
	if err := applyUsersEnv(cfg, false); err != nil {
		t.Fatalf("applyUsersEnv() lenient error = %v", err)
	}
}

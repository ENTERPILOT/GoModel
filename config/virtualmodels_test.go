package config

import (
	"strings"
	"testing"
)

func TestApplyVirtualModelsEnv_ParsesAndMerges(t *testing.T) {
	cfg := &Config{VirtualModels: []VirtualModelConfig{
		{Source: "smart", Target: "openai/gpt-4o"},
		{Source: "keep", Target: "groq/llama"},
	}}
	t.Setenv(envVirtualModels, `[
		{"source":"smart","strategy":"cost","targets":[{"model":"openai/gpt-4o"},{"model":"groq/llama"}]},
		{"source":"new","target":"anthropic/claude"}
	]`)

	if err := applyVirtualModelsEnv(cfg); err != nil {
		t.Fatalf("applyVirtualModelsEnv() error = %v", err)
	}
	if len(cfg.VirtualModels) != 3 {
		t.Fatalf("merged len = %d, want 3", len(cfg.VirtualModels))
	}
	// "smart" is overridden in place (env wins) and keeps its position.
	smart := cfg.VirtualModels[0]
	if smart.Source != "smart" || smart.Strategy != "cost" || len(smart.Targets) != 2 {
		t.Fatalf("env did not override smart: %#v", smart)
	}
	// "keep" is untouched; "new" is appended.
	if cfg.VirtualModels[1].Source != "keep" || cfg.VirtualModels[2].Source != "new" {
		t.Fatalf("merge order wrong: %#v", cfg.VirtualModels)
	}
}

func TestApplyVirtualModelsEnv_Invalid(t *testing.T) {
	cfg := &Config{}
	t.Setenv(envVirtualModels, `{not valid json`)
	if err := applyVirtualModelsEnv(cfg); err == nil {
		t.Fatalf("applyVirtualModelsEnv() error = nil, want parse error")
	}
}

// The env layer overrides YAML entry by entry, so a typo must fail loudly rather
// than let a malformed env entry silently win over a correct YAML one.
func TestApplyVirtualModelsEnv_RejectsUnknownField(t *testing.T) {
	cfg := &Config{}
	t.Setenv(envVirtualModels, `[{"source":"smart","targts":[{"model":"openai/gpt-4o"}]}]`)

	err := applyVirtualModelsEnv(cfg)
	if err == nil {
		t.Fatal("applyVirtualModelsEnv() error = nil, want unknown-field error")
	}
	if !strings.Contains(err.Error(), "targts") {
		t.Fatalf("applyVirtualModelsEnv() error = %q, want it to name the unknown field", err)
	}
}

func TestApplyVirtualModelsEnv_Unset(t *testing.T) {
	cfg := &Config{VirtualModels: []VirtualModelConfig{{Source: "smart", Target: "openai/gpt-4o"}}}
	t.Setenv(envVirtualModels, "")
	if err := applyVirtualModelsEnv(cfg); err != nil {
		t.Fatalf("applyVirtualModelsEnv() error = %v", err)
	}
	if len(cfg.VirtualModels) != 1 {
		t.Fatalf("unset env mutated config: %#v", cfg.VirtualModels)
	}
}

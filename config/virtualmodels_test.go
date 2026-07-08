package config

import (
	"fmt"
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

	if err := applyVirtualModelsEnv(cfg, true); err != nil {
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
	if err := applyVirtualModelsEnv(cfg, true); err == nil {
		t.Fatalf("applyVirtualModelsEnv() error = nil, want parse error")
	}
}

// The env layer overrides YAML entry by entry, so a typo must fail loudly rather
// than let a malformed env entry silently win over a correct YAML one.
func TestApplyVirtualModelsEnv_RejectsUnknownField(t *testing.T) {
	cfg := &Config{}
	t.Setenv(envVirtualModels, `[{"source":"smart","targts":[{"model":"openai/gpt-4o"}]}]`)

	err := applyVirtualModelsEnv(cfg, true)
	if err == nil {
		t.Fatal("applyVirtualModelsEnv() error = nil, want unknown-field error")
	}
	if !strings.Contains(err.Error(), "targts") {
		t.Fatalf("applyVirtualModelsEnv() error = %q, want it to name the unknown field", err)
	}
}

// json.Decoder stops after the first value and leaves the rest unread, so trailing
// data must be rejected explicitly — silently applying half an env var is the failure
// this path exists to prevent. Structural, therefore fatal in both modes.
func TestApplyVirtualModelsEnv_RejectsTrailingData(t *testing.T) {
	trailing := map[string]string{
		"garbage suffix":        `[{"source":"smart","target":"openai/gpt-4o"}] and then some junk`,
		"second JSON value":     `[{"source":"smart","target":"openai/gpt-4o"}] {"targts":[]}`,
		"second JSON on a line": "[{\"source\":\"smart\",\"target\":\"openai/gpt-4o\"}]\n{\"x\":1}",
	}
	for name, raw := range trailing {
		for _, strict := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/strict=%v", name, strict), func(t *testing.T) {
				cfg := &Config{}
				t.Setenv(envVirtualModels, raw)

				err := applyVirtualModelsEnv(cfg, strict)
				if err == nil {
					t.Fatal("applyVirtualModelsEnv() error = nil, want trailing-data error")
				}
				if !strings.Contains(err.Error(), "unexpected data after the JSON value") {
					t.Fatalf("applyVirtualModelsEnv() error = %q, want a trailing-data error", err)
				}
			})
		}
	}
}

// CONFIG_STRICT=false applies to the env layer too: the unknown key is warned about
// and the rest of the entry still loads.
func TestApplyVirtualModelsEnv_LaxIgnoresUnknownField(t *testing.T) {
	cfg := &Config{}
	t.Setenv(envVirtualModels, `[{"source":"smart","targts":[],"target":"openai/gpt-4o"}]`)

	if err := applyVirtualModelsEnv(cfg, false); err != nil {
		t.Fatalf("applyVirtualModelsEnv() error = %v, want the unknown key ignored", err)
	}
	if len(cfg.VirtualModels) != 1 || cfg.VirtualModels[0].Target != "openai/gpt-4o" {
		t.Fatalf("lax decode dropped the known fields: %#v", cfg.VirtualModels)
	}
}

// Even lax, a value of the wrong type is fatal.
func TestApplyVirtualModelsEnv_LaxStillRejectsMalformedValues(t *testing.T) {
	cfg := &Config{}
	t.Setenv(envVirtualModels, `[{"source":123}]`)

	if err := applyVirtualModelsEnv(cfg, false); err == nil {
		t.Fatal("applyVirtualModelsEnv() error = nil, want a type error")
	}
}

func TestApplyVirtualModelsEnv_Unset(t *testing.T) {
	cfg := &Config{VirtualModels: []VirtualModelConfig{{Source: "smart", Target: "openai/gpt-4o"}}}
	t.Setenv(envVirtualModels, "")
	if err := applyVirtualModelsEnv(cfg, true); err != nil {
		t.Fatalf("applyVirtualModelsEnv() error = %v", err)
	}
	if len(cfg.VirtualModels) != 1 {
		t.Fatalf("unset env mutated config: %#v", cfg.VirtualModels)
	}
}

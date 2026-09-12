package config

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
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
	err := applyVirtualModelsEnv(cfg, true)
	require.NoError(t, err)
	require.Len(t, cfg.VirtualModels, 3)

	// "smart" is overridden in place (env wins) and keeps its position.
	smart := cfg.VirtualModels[0]
	require.Equal(t, "smart", smart.Source)
	require.Equal(t, "cost", smart.Strategy)
	require.Len(t, smart.Targets, 2, "env did not override smart: %#v", smart)

	// "keep" is untouched; "new" is appended.
	require.Equal(t, "keep", cfg.VirtualModels[1].Source)
	require.Equal(t, "new", cfg.VirtualModels[2].Source, "merge order wrong: %#v", cfg.VirtualModels)
}

func TestApplyVirtualModelsEnv_Invalid(t *testing.T) {
	cfg := &Config{}
	t.Setenv(envVirtualModels, `{not valid json`)
	require.Error(t, applyVirtualModelsEnv(cfg, true))
}

// The env layer overrides YAML entry by entry, so a typo must fail loudly rather
// than let a malformed env entry silently win over a correct YAML one.
func TestApplyVirtualModelsEnv_RejectsUnknownField(t *testing.T) {
	cfg := &Config{}
	t.Setenv(envVirtualModels, `[{"source":"smart","targts":[{"model":"openai/gpt-4o"}]}]`)

	err := applyVirtualModelsEnv(cfg, true)
	require.Error(t, err)
	require.Contains(t, err.Error(), "targts")
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
				require.Error(t, err)
				require.Contains(t, err.Error(), "unexpected data after the JSON value")
			})
		}
	}
}

// CONFIG_STRICT=false applies to the env layer too: the unknown key is warned about
// and the rest of the entry still loads.
func TestApplyVirtualModelsEnv_LaxIgnoresUnknownField(t *testing.T) {
	cfg := &Config{}
	t.Setenv(envVirtualModels, `[{"source":"smart","targts":[],"target":"openai/gpt-4o"}]`)
	err := applyVirtualModelsEnv(cfg, false)
	require.NoError(t, err)
	require.Len(t, cfg.VirtualModels, 1)
	require.Equal(t, "openai/gpt-4o", cfg.VirtualModels[0].Target)
}

// Even lax, a value of the wrong type is fatal.
func TestApplyVirtualModelsEnv_LaxStillRejectsMalformedValues(t *testing.T) {
	cfg := &Config{}
	t.Setenv(envVirtualModels, `[{"source":123}]`)

	require.Error(t, applyVirtualModelsEnv(cfg, false))
}

func TestApplyVirtualModelsEnv_Unset(t *testing.T) {
	cfg := &Config{VirtualModels: []VirtualModelConfig{{Source: "smart", Target: "openai/gpt-4o"}}}
	t.Setenv(envVirtualModels, "")
	err := applyVirtualModelsEnv(cfg, true)
	require.NoError(t, err)
	require.Len(t, cfg.VirtualModels, 1)
}

func TestVirtualModelConfig_PluginStrategyFromYAMLAndEnv(t *testing.T) {
	var cfg Config
	err := yaml.Unmarshal([]byte(`
virtual_models:
  - source: smart-router
    strategy: plugin
    strategy_plugin: cheapest_healthy
    strategy_config:
      prefer: fastest
      max_error_rate: 0.1
    targets:
      - { model: openai/gpt-4o }
      - { model: groq/llama }
`), &cfg)
	require.NoError(t, err)
	require.Len(t, cfg.VirtualModels, 1)

	vm := cfg.VirtualModels[0]
	require.Equal(t, "plugin", vm.Strategy)
	require.Equal(t, "cheapest_healthy", vm.StrategyPlugin, "yaml entry = %+v, want plugin strategy fields", vm)
	want := map[string]any{"prefer": "fastest", "max_error_rate": 0.1}
	require.Equal(t, want, vm.StrategyConfig)

	t.Setenv(envVirtualModels, `[{"source":"smart-router","strategy":"plugin","strategy_plugin":"latency_aware","strategy_config":{"p95_window":"5m"},"targets":[{"model":"openai/gpt-4o"},{"model":"groq/llama"}]}]`)
	err = applyVirtualModelsEnv(&cfg, true)
	require.NoError(t, err)

	vm = cfg.VirtualModels[0]
	require.Equal(t, "latency_aware", vm.StrategyPlugin)
	require.Equal(t, map[string]any{"p95_window": "5m"}, vm.StrategyConfig, "env entry = %+v, want env to override plugin fields", vm)
}

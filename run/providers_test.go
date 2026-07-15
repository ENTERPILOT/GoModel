package run

import (
	"slices"
	"testing"

	"github.com/enterpilot/gomodel/config"
)

func TestDefaultProviderFactoryRegistersAllProviderTypes(t *testing.T) {
	expected := []string{
		"anthropic", "azure", "bailian", "bedrock", "deepseek", "fireworks",
		"gemini", "groq", "kilo", "kimicode", "meta", "minimax", "ollama", "openai", "opencode_go",
		"openrouter", "oracle", "vertex", "vllm", "xai", "xiaomi", "zai",
	}

	for _, metricsEnabled := range []bool{false, true} {
		cfg := &config.Config{}
		cfg.Metrics.Enabled = metricsEnabled

		factory := defaultProviderFactory(cfg)
		got := factory.RegisteredTypes()
		slices.Sort(got)

		if !slices.Equal(got, expected) {
			t.Errorf("metrics=%v: registered types = %v, want %v", metricsEnabled, got, expected)
		}
	}
}

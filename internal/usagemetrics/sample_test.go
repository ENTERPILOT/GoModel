package usagemetrics

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/enterpilot/gomodel/internal/usage"
)

func TestNewSample(t *testing.T) {
	tests := []struct {
		name  string
		entry usage.UsageEntry
		want  Sample
	}{
		{
			name: "plain call with provider and billed cost",
			entry: usage.UsageEntry{
				Provider: "openai", ProviderName: "openai-eu", Model: "gpt-5", RoutedModel: "gpt-5-latest",
				Endpoint: "/v1/chat/completions", InputTokens: 100, OutputTokens: 20,
				TotalCost: new(0.4), BilledCost: new(0.5),
			},
			want: Sample{
				Provider: "openai-eu", ProviderType: "openai", Model: "gpt-5", RequestModel: "gpt-5-latest",
				Endpoint: "/v1/chat/completions", InputTokens: 100, OutputTokens: 20,
				ProviderCost: 0.4, BilledCost: 0.5,
			},
		},
		{
			name:  "provider type stands in for a missing provider name",
			entry: usage.UsageEntry{Provider: "openai", Model: "gpt-5", InputTokens: 1},
			want:  Sample{Provider: "openai", ProviderType: "openai", Model: "gpt-5", InputTokens: 1},
		},
		{
			name: "openai cached and reasoning tokens are parts of the totals",
			entry: usage.UsageEntry{
				Provider: "openai", Model: "gpt-5", InputTokens: 100, OutputTokens: 50,
				RawData: map[string]any{"prompt_cached_tokens": 60, "completion_reasoning_tokens": 30},
			},
			want: Sample{
				Provider: "openai", ProviderType: "openai", Model: "gpt-5",
				InputTokens: 100, OutputTokens: 50, CachedInputTokens: 60, ReasoningOutputTokens: 30,
			},
		},
		{
			name: "openai cache writes are part of input",
			entry: usage.UsageEntry{
				Provider: "openai", Model: "gpt-5", InputTokens: 100, OutputTokens: 5,
				RawData: map[string]any{"prompt_cached_tokens": 60, "prompt_cache_write_tokens": 25},
			},
			want: Sample{
				Provider: "openai", ProviderType: "openai", Model: "gpt-5",
				InputTokens: 100, OutputTokens: 5, CachedInputTokens: 60, CacheWriteInputTokens: 25,
			},
		},
		{
			name: "anthropic reports cache reads and writes on top of input",
			entry: usage.UsageEntry{
				Provider: "anthropic", Model: "claude", InputTokens: 10, OutputTokens: 5,
				RawData: map[string]any{"cache_read_input_tokens": 200, "cache_creation_input_tokens": 40},
			},
			want: Sample{
				Provider: "anthropic", ProviderType: "anthropic", Model: "claude",
				InputTokens: 250, OutputTokens: 5, CachedInputTokens: 200, CacheWriteInputTokens: 40,
			},
		},
		{
			name: "xai reports reasoning on top of output",
			entry: usage.UsageEntry{
				Provider: "xai", Model: "grok", InputTokens: 10, OutputTokens: 5,
				RawData: map[string]any{"reasoning_tokens": 30},
			},
			want: Sample{
				Provider: "xai", ProviderType: "xai", Model: "grok",
				InputTokens: 10, OutputTokens: 35, ReasoningOutputTokens: 30,
			},
		},
		{
			name:  "unknown cost stays zero",
			entry: usage.UsageEntry{Provider: "ollama", Model: "llama", InputTokens: 3, OutputTokens: 4},
			want:  Sample{Provider: "ollama", ProviderType: "ollama", Model: "llama", InputTokens: 3, OutputTokens: 4},
		},
		{
			name: "response-cache hit keeps only its billed cost",
			entry: usage.UsageEntry{
				Provider: "openai", Model: "gpt-5", CacheType: usage.CacheTypeExact, InputTokens: 100, OutputTokens: 20,
				RawData:   map[string]any{"prompt_cached_tokens": 60},
				TotalCost: new(0.4), BilledCost: new(0.5),
			},
			want: Sample{Provider: "openai", ProviderType: "openai", Model: "gpt-5", BilledCost: 0.5},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, newSample(&tt.entry))
		})
	}
}

func TestSampleEmpty(t *testing.T) {
	tests := []struct {
		name   string
		sample Sample
		want   bool
	}{
		{name: "mcp tool call", sample: Sample{Provider: "github", ProviderType: "mcp", Model: "search"}, want: true},
		{name: "unbilled response-cache hit", sample: newSample(&usage.UsageEntry{CacheType: usage.CacheTypeSemantic, InputTokens: 9, BilledCost: new(0.0)}), want: true},
		{name: "tokens", sample: Sample{OutputTokens: 1}},
		{name: "provider cost without tokens", sample: Sample{ProviderCost: 0.01}},
		{name: "billed cost only", sample: Sample{BilledCost: 0.01}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.sample.empty())
		})
	}
}

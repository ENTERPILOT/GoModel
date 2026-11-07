package virtualmodels

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// visionCatalog is balancingCatalog with openai/gpt-4o and anthropic/claude
// reporting the vision capability, groq/llama reporting none, and
// local/mistral carrying no metadata at all.
func visionCatalog() fakeCatalog {
	catalog := balancingCatalog()
	for _, model := range []string{"openai/gpt-4o", "anthropic/claude"} {
		entry := catalog.supported[model]
		entry.Metadata.Capabilities = map[string]bool{"vision": true}
		catalog.supported[model] = entry
	}
	llama := catalog.supported["groq/llama"]
	llama.Metadata.Capabilities = map[string]bool{"vision": false, "function_calling": true}
	catalog.supported["groq/llama"] = llama
	return catalog
}

// imageContext reports whether the request carries images through the same
// lazy probe the server installs, counting how often it runs.
func imageContext(images bool, calls *int) context.Context {
	return core.WithImageInputProbe(context.Background(), func() bool {
		*calls++
		return images
	})
}

func upsertVisionRedirect(t *testing.T, svc *Service, source, strategy string, vision bool, models ...string) {
	t.Helper()
	targets := make([]Target, len(models))
	for i, model := range models {
		targets[i] = Target{Model: model}
	}
	err := svc.Upsert(context.Background(), VirtualModel{
		Source: source, Targets: targets, Strategy: strategy, VisionRouting: vision, Enabled: true,
	})
	require.NoError(t, err)
}

func resolveWithContext(t *testing.T, svc *Service, ctx context.Context, source string, n int) []string {
	t.Helper()
	out := make([]string, 0, n)
	for range n {
		sel, _, err := svc.ResolveModelForUserPath(ctx, core.NewRequestedModelSelector(source, ""))
		require.NoError(t, err)
		out = append(out, sel.QualifiedModel())
	}
	return out
}

func TestVisionRouting_FiltersTargetsBeforeStrategy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		strategy string
		vision   bool
		images   bool
		targets  []string
		want     []string
	}{
		{
			name:     "failover skips a text-only primary for an image request",
			strategy: StrategyFailover, vision: true, images: true,
			targets: []string{"groq/llama", "openai/gpt-4o"},
			want:    []string{"openai/gpt-4o", "openai/gpt-4o"},
		},
		{
			name:     "text request keeps the primary",
			strategy: StrategyFailover, vision: true, images: false,
			targets: []string{"groq/llama", "openai/gpt-4o"},
			want:    []string{"groq/llama", "groq/llama"},
		},
		{
			name:     "vision routing off keeps today's routing",
			strategy: StrategyFailover, vision: false, images: true,
			targets: []string{"groq/llama", "openai/gpt-4o"},
			want:    []string{"groq/llama", "groq/llama"},
		},
		{
			name:     "cost picks the cheapest vision target",
			strategy: StrategyCost, vision: true, images: true,
			targets: []string{"groq/llama", "anthropic/claude", "openai/gpt-4o"},
			want:    []string{"openai/gpt-4o", "openai/gpt-4o"},
		},
		{
			name:     "round robin rotates over vision targets only",
			strategy: StrategyRoundRobin, vision: true, images: true,
			targets: []string{"groq/llama", "openai/gpt-4o", "local/mistral", "anthropic/claude"},
			want:    []string{"openai/gpt-4o", "anthropic/claude", "openai/gpt-4o"},
		},
		{
			name:     "unknown capability counts as no image input",
			strategy: StrategyFailover, vision: true, images: true,
			targets: []string{"local/mistral", "anthropic/claude"},
			want:    []string{"anthropic/claude"},
		},
		{
			name:     "no vision target keeps every target",
			strategy: StrategyFailover, vision: true, images: true,
			targets: []string{"groq/llama", "local/mistral"},
			want:    []string{"groq/llama", "groq/llama"},
		},
	}
	// One service for every case: the SQLite test store is keyed by a
	// truncated test name, so per-subtest stores would collide.
	svc, err := NewService(newSQLVMStore(t), visionCatalog(), true)
	require.NoError(t, err)
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := fmt.Sprintf("smart-%d", i)
			upsertVisionRedirect(t, svc, source, tt.strategy, tt.vision, tt.targets...)

			calls := 0
			got := resolveWithContext(t, svc, imageContext(tt.images, &calls), source, len(tt.want))
			assert.Equal(t, tt.want, got)
			if !tt.vision {
				assert.Zero(t, calls, "the body is never inspected without vision routing")
			}
		})
	}
}

func TestVisionRouting_CarriesDownChains(t *testing.T) {
	t.Parallel()
	svc, err := NewService(newSQLVMStore(t), visionCatalog(), true)
	require.NoError(t, err)
	// The inner redirect does not opt in: the outer decision still applies to
	// it, so the round robin inside never lands on the text-only model.
	upsertVisionRedirect(t, svc, "cheap", StrategyRoundRobin, false, "groq/llama", "openai/gpt-4o")
	upsertVisionRedirect(t, svc, "text", StrategyRoundRobin, false, "groq/llama", "local/mistral")
	upsertVisionRedirect(t, svc, "smart", StrategyFailover, true, "text", "cheap", "anthropic/claude")

	calls := 0
	got := resolveWithContext(t, svc, imageContext(true, &calls), "smart", 3)
	assert.Equal(t, []string{"openai/gpt-4o", "openai/gpt-4o", "openai/gpt-4o"}, got)

	got = resolveWithContext(t, svc, imageContext(false, &calls), "smart", 2)
	assert.Equal(t, []string{"groq/llama", "local/mistral"}, got)
}

func TestVisionRouting_RoundTripsThroughStoreAndView(t *testing.T) {
	t.Parallel()
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		require.NoError(t, store.Upsert(ctx, VirtualModel{
			Source:        "smart",
			Targets:       []Target{{Model: "openai/gpt-4o"}, {Model: "groq/llama"}},
			VisionRouting: true,
			Enabled:       true,
		}))
		got, err := store.Get(ctx, "smart")
		require.NoError(t, err)
		assert.True(t, got.VisionRouting)

		require.NoError(t, store.Upsert(ctx, VirtualModel{
			Source:  "smart",
			Targets: []Target{{Model: "openai/gpt-4o"}, {Model: "groq/llama"}},
			Enabled: true,
		}))
		got, err = store.Get(ctx, "smart")
		require.NoError(t, err)
		assert.False(t, got.VisionRouting)
	})

	svc, err := NewService(newSQLVMStore(t), visionCatalog(), true)
	require.NoError(t, err)
	upsertVisionRedirect(t, svc, "smart", "", true, "openai/gpt-4o", "groq/llama")
	views := svc.ListViews()
	require.Len(t, views, 1)
	assert.True(t, views[0].VisionRouting)
}

func TestVisionRouting_RoutesImageInputOnlyWhenEnabled(t *testing.T) {
	t.Parallel()
	svc, err := NewService(newSQLVMStore(t), visionCatalog(), true)
	require.NoError(t, err)
	assert.False(t, svc.RoutesImageInput())

	upsertVisionRedirect(t, svc, "plain", "", false, "openai/gpt-4o", "groq/llama")
	assert.False(t, svc.RoutesImageInput())

	require.NoError(t, svc.Upsert(context.Background(), VirtualModel{
		Source: "off", Targets: []Target{{Model: "openai/gpt-4o"}, {Model: "groq/llama"}},
		VisionRouting: true, Enabled: false,
	}))
	assert.False(t, svc.RoutesImageInput(), "a disabled redirect does not count")

	upsertVisionRedirect(t, svc, "smart", "", true, "openai/gpt-4o", "groq/llama")
	assert.True(t, svc.RoutesImageInput())
}

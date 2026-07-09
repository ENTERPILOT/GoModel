package providers

import (
	"fmt"
	"testing"

	"gomodel/internal/core"
)

// buildBenchRegistry creates a registry holding exactly totalModels models,
// distributed round-robin across providersN providers, mirroring a realistic
// multi-provider catalog. Model IDs are globally unique (model-<i>) so the count
// is exact regardless of how it divides across providers.
func buildBenchRegistry(providersN, totalModels int) *ModelRegistry {
	provs := make([]*mockProvider, providersN)
	for p := range provs {
		provs[p] = &mockProvider{name: fmt.Sprintf("prov%d", p)}
	}
	entries := make([]registryModelEntry, 0, totalModels)
	for i := range totalModels {
		p := i % providersN
		entries = append(entries, registryModelEntry{
			provider:     provs[p],
			providerName: provs[p].name,
			providerType: provs[p].name,
			modelID:      fmt.Sprintf("model-%d", i),
		})
	}
	return newTestRegistryWithModels(entries...)
}

// benchSelector returns a "<provider>/<model>" selector that exists in a registry
// built by buildBenchRegistry(providersN, totalModels), picking a mid-catalog
// model. Position is irrelevant to the O(1) index but the model must exist.
func benchSelector(providersN, totalModels int) string {
	mid := totalModels / 2
	return fmt.Sprintf("prov%d/model-%d", mid%providersN, mid)
}

// BenchmarkResolvePerRequest simulates the resolution calls a single chat
// request makes through the Router against a populated catalog: ResolveModel +
// Supports + GetProviderType + GetProviderName (the ~per-request fan-out).
func BenchmarkResolvePerRequest(b *testing.B) {
	for _, n := range []int{50, 300, 1000} {
		b.Run(fmt.Sprintf("models=%d", n), func(b *testing.B) {
			reg := buildBenchRegistry(6, n)
			router, err := NewRouter(reg)
			if err != nil {
				b.Fatalf("NewRouter: %v", err)
			}
			// A mid-catalog qualified selector, the common production case.
			sel := benchSelector(6, n)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				requested := core.NewRequestedModelSelector(sel, "")
				if _, _, err := router.ResolveModel(requested); err != nil {
					b.Fatalf("ResolveModel: %v", err)
				}
				_ = router.Supports(sel)
				_ = router.GetProviderType(sel)
				_ = router.GetProviderName(sel)
			}
		})
	}
}

// BenchmarkListModelsWithProvider isolates the full-catalog defensive copy.
func BenchmarkListModelsWithProvider(b *testing.B) {
	for _, n := range []int{50, 300, 1000} {
		b.Run(fmt.Sprintf("models=%d", n), func(b *testing.B) {
			reg := buildBenchRegistry(6, n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = reg.ListModelsWithProvider()
			}
		})
	}
}

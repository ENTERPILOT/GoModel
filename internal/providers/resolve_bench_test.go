package providers

import (
	"fmt"
	"testing"

	"gomodel/internal/core"
)

// buildBenchRegistry creates a registry with `providersN` providers each holding
// `perProvider` models, mirroring a realistic multi-provider catalog.
func buildBenchRegistry(providersN, perProvider int) *ModelRegistry {
	entries := make([]registryModelEntry, 0, providersN*perProvider)
	for p := 0; p < providersN; p++ {
		name := fmt.Sprintf("prov%d", p)
		prov := &mockProvider{name: name}
		for m := 0; m < perProvider; m++ {
			entries = append(entries, registryModelEntry{
				provider:     prov,
				providerName: name,
				providerType: name,
				modelID:      fmt.Sprintf("model-%d-%d", p, m),
			})
		}
	}
	return newTestRegistryWithModels(entries...)
}

// BenchmarkResolvePerRequest simulates the resolution calls a single chat
// request makes through the Router against a populated catalog: ResolveModel +
// Supports + GetProviderType + GetProviderName (the ~per-request fan-out).
func BenchmarkResolvePerRequest(b *testing.B) {
	for _, n := range []int{50, 300, 1000} {
		b.Run(fmt.Sprintf("models=%d", n), func(b *testing.B) {
			reg := buildBenchRegistry(6, n/6)
			router, err := NewRouter(reg)
			if err != nil {
				b.Fatalf("NewRouter: %v", err)
			}
			// A mid-catalog qualified selector, the common production case.
			sel := fmt.Sprintf("prov3/model-3-%d", (n/6)/2)

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
			reg := buildBenchRegistry(6, n/6)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = reg.ListModelsWithProvider()
			}
		})
	}
}

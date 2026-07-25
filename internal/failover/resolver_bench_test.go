package failover

import (
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

// BenchmarkResolveFailovers covers the path every translated request takes
// when failover rules exist. It exists because the resolver used to compute
// the request's match keys twice — once for the disabled check, once for the
// rule lookup — which this measures at 8 allocs/op against 6 today.
func BenchmarkResolveFailovers(b *testing.B) {
	registry := newFakeRegistry(
		modelInfo("gpt-4o", "openai", "openai", 1287, "gpt-4o"),
		modelInfo("gpt-4o", "azure", "azure", 1287, "gpt-4o"),
		modelInfo("gemini-2.5-pro", "gemini", "gemini", 1290, "gemini-2.5-pro"),
	)
	resolver := NewResolver(config.FailoverConfig{
		Enabled: true,
		Manual:  map[string][]string{"gpt-4o": {"azure/gpt-4o", "gemini/gemini-2.5-pro"}},
	}, registry)

	resolution := &core.RequestModelResolution{
		Requested:        core.NewRequestedModelSelector("gpt-4o", ""),
		ResolvedSelector: core.ModelSelector{Model: "gpt-4o"},
		ProviderType:     "openai",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = resolver.ResolveFailovers(resolution, core.OperationChatCompletions)
	}
}

package run

import (
	"gomodel/config"
	"gomodel/internal/observability"
	"gomodel/internal/providers"
	"gomodel/internal/providers/anthropic"
	"gomodel/internal/providers/azure"
	"gomodel/internal/providers/bailian"
	"gomodel/internal/providers/bedrock"
	"gomodel/internal/providers/deepseek"
	"gomodel/internal/providers/fireworks"
	"gomodel/internal/providers/gemini"
	"gomodel/internal/providers/groq"
	"gomodel/internal/providers/kimicode"
	"gomodel/internal/providers/minimax"
	"gomodel/internal/providers/ollama"
	"gomodel/internal/providers/openai"
	"gomodel/internal/providers/opencodego"
	"gomodel/internal/providers/openrouter"
	"gomodel/internal/providers/oracle"
	"gomodel/internal/providers/vertex"
	"gomodel/internal/providers/vllm"
	"gomodel/internal/providers/xai"
	"gomodel/internal/providers/xiaomi"
	"gomodel/internal/providers/zai"
)

// defaultProviderFactory builds the provider factory with every provider type
// the standard gateway ships with.
func defaultProviderFactory(cfg *config.Config) *providers.ProviderFactory {
	factory := providers.NewProviderFactory()

	if cfg.Metrics.Enabled {
		factory.SetHooks(observability.NewPrometheusHooks())
	}

	factory.Add(openai.Registration)
	factory.Add(openrouter.Registration)
	factory.Add(azure.Registration)
	factory.Add(bailian.Registration)
	factory.Add(oracle.Registration)
	factory.Add(anthropic.Registration)
	factory.Add(bedrock.Registration)
	factory.Add(deepseek.Registration)
	factory.Add(fireworks.Registration)
	factory.Add(gemini.Registration)
	factory.Add(vertex.Registration)
	factory.Add(groq.Registration)
	factory.Add(kimicode.Registration)
	factory.Add(minimax.Registration)
	factory.Add(ollama.Registration)
	factory.Add(opencodego.Registration)
	factory.Add(vllm.Registration)
	factory.Add(xai.Registration)
	factory.Add(xiaomi.Registration)
	factory.Add(zai.Registration)

	return factory
}

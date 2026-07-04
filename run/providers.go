package run

import (
	"github.com/ENTERPILOT/GoModel/config"
	"github.com/ENTERPILOT/GoModel/internal/observability"
	"github.com/ENTERPILOT/GoModel/internal/providers"
	"github.com/ENTERPILOT/GoModel/internal/providers/anthropic"
	"github.com/ENTERPILOT/GoModel/internal/providers/azure"
	"github.com/ENTERPILOT/GoModel/internal/providers/bailian"
	"github.com/ENTERPILOT/GoModel/internal/providers/bedrock"
	"github.com/ENTERPILOT/GoModel/internal/providers/deepseek"
	"github.com/ENTERPILOT/GoModel/internal/providers/fireworks"
	"github.com/ENTERPILOT/GoModel/internal/providers/gemini"
	"github.com/ENTERPILOT/GoModel/internal/providers/groq"
	"github.com/ENTERPILOT/GoModel/internal/providers/minimax"
	"github.com/ENTERPILOT/GoModel/internal/providers/ollama"
	"github.com/ENTERPILOT/GoModel/internal/providers/openai"
	"github.com/ENTERPILOT/GoModel/internal/providers/opencodego"
	"github.com/ENTERPILOT/GoModel/internal/providers/openrouter"
	"github.com/ENTERPILOT/GoModel/internal/providers/oracle"
	"github.com/ENTERPILOT/GoModel/internal/providers/vertex"
	"github.com/ENTERPILOT/GoModel/internal/providers/vllm"
	"github.com/ENTERPILOT/GoModel/internal/providers/xai"
	"github.com/ENTERPILOT/GoModel/internal/providers/xiaomi"
	"github.com/ENTERPILOT/GoModel/internal/providers/zai"
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
	factory.Add(minimax.Registration)
	factory.Add(ollama.Registration)
	factory.Add(opencodego.Registration)
	factory.Add(vllm.Registration)
	factory.Add(xai.Registration)
	factory.Add(xiaomi.Registration)
	factory.Add(zai.Registration)

	return factory
}

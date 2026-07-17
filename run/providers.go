package run

import (
	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/observability"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/anthropic"
	"github.com/enterpilot/gomodel/internal/providers/azure"
	"github.com/enterpilot/gomodel/internal/providers/bailian"
	"github.com/enterpilot/gomodel/internal/providers/bedrock"
	"github.com/enterpilot/gomodel/internal/providers/bedrockmantle"
	"github.com/enterpilot/gomodel/internal/providers/deepseek"
	"github.com/enterpilot/gomodel/internal/providers/fireworks"
	"github.com/enterpilot/gomodel/internal/providers/gemini"
	"github.com/enterpilot/gomodel/internal/providers/groq"
	"github.com/enterpilot/gomodel/internal/providers/kilo"
	"github.com/enterpilot/gomodel/internal/providers/kimicode"
	"github.com/enterpilot/gomodel/internal/providers/meta"
	"github.com/enterpilot/gomodel/internal/providers/minimax"
	"github.com/enterpilot/gomodel/internal/providers/ollama"
	"github.com/enterpilot/gomodel/internal/providers/openai"
	"github.com/enterpilot/gomodel/internal/providers/opencodego"
	"github.com/enterpilot/gomodel/internal/providers/openrouter"
	"github.com/enterpilot/gomodel/internal/providers/oracle"
	"github.com/enterpilot/gomodel/internal/providers/vertex"
	"github.com/enterpilot/gomodel/internal/providers/vllm"
	"github.com/enterpilot/gomodel/internal/providers/xai"
	"github.com/enterpilot/gomodel/internal/providers/xiaomi"
	"github.com/enterpilot/gomodel/internal/providers/zai"
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
	factory.Add(bedrockmantle.Registration)
	factory.Add(deepseek.Registration)
	factory.Add(fireworks.Registration)
	factory.Add(gemini.Registration)
	factory.Add(vertex.Registration)
	factory.Add(groq.Registration)
	factory.Add(kilo.Registration)
	factory.Add(kimicode.Registration)
	factory.Add(meta.Registration)
	factory.Add(minimax.Registration)
	factory.Add(ollama.Registration)
	factory.Add(opencodego.Registration)
	factory.Add(vllm.Registration)
	factory.Add(xai.Registration)
	factory.Add(xiaomi.Registration)
	factory.Add(zai.Registration)

	return factory
}

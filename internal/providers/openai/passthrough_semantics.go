package openai

import "github.com/enterpilot/gomodel/internal/providers"

var passthroughSemanticEnricher = providers.NewOpenAICompatibleSemanticEnricher("openai")

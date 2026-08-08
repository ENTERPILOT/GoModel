package zai

import "github.com/enterpilot/gomodel/internal/providers"

var passthroughSemanticEnricher = providers.NewOpenAICompatibleSemanticEnricher("zai")

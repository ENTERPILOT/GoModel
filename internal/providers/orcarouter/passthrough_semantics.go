package orcarouter

import "github.com/enterpilot/gomodel/internal/providers"

var passthroughSemanticEnricher = providers.NewOpenAICompatibleSemanticEnricher("orcarouter")

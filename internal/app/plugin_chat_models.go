package app

import (
	"context"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/gateway"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/internal/providers"
)

// pluginChatModels checks the models plugins send inference to against the
// catalog: virtual models are resolved first, then the resolved model's
// catalog entry decides (see core.ChatUnsupportedReason).
type pluginChatModels struct {
	router   *providers.Router
	resolver gateway.ModelResolver
}

var _ plugins.ChatModelChecker = pluginChatModels{}

func (m pluginChatModels) ChatUnsupportedReason(ctx context.Context, model string) string {
	if m.router == nil {
		return ""
	}
	selector, _, err := gateway.ResolveExecutionSelector(ctx, m.router, m.resolver, core.NewRequestedModelSelector(model, ""))
	if err != nil || selector.Model == "" {
		return ""
	}
	info, ok := m.router.LookupModel(selector.QualifiedModel())
	if !ok || info == nil {
		return ""
	}
	return core.ChatUnsupportedReason(info.Metadata)
}

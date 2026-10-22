package app

import (
	"context"
	"fmt"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/internal/providers"
)

// pluginChatModels checks a plugin's model field when a guardrail is saved: a
// model the catalog describes as unable to chat is refused (see
// core.ChatUnsupportedReason). A virtual model is not resolved here, since
// resolving one picks a target and would advance its load balancing;
// chatModelGuard checks the model each call resolves to instead.
type pluginChatModels struct {
	router *providers.Router
}

var _ plugins.ChatModelChecker = pluginChatModels{}

func (m pluginChatModels) ChatUnsupportedReason(_ context.Context, model string) string {
	if m.router == nil {
		return ""
	}
	return chatUnsupportedReason(m.router, model)
}

// chatUnsupportedReason canonicalizes model through the router first, so a
// selector naming a provider type ("jev/kev-latest" for a provider named
// "kev") finds its catalog entry. The router resolves concrete selectors
// only, never virtual models.
func chatUnsupportedReason(router *providers.Router, model string) string {
	if selector, _, err := router.ResolveModel(core.NewRequestedModelSelector(model, "")); err == nil {
		model = selector.QualifiedModel()
	}
	info, ok := router.LookupModel(model)
	if !ok || info == nil {
		return ""
	}
	return core.ChatUnsupportedReason(info.Metadata)
}

// chatModelGuard is the router plugin inference reaches once virtual models
// are resolved: the internal chat executor's provider, and the startup
// executor's before that. It sees the concrete model of each attempt, under
// the plugin call's own user path, and adds no selection of its own. It
// refuses a model that cannot chat, such as a decision model behind a
// virtual model, before the provider call: that provider would fail every
// call, and the instance's fail mode would then allow or block all traffic.
type chatModelGuard struct {
	*providers.Router
}

func (g chatModelGuard) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	if req != nil {
		model := core.ModelSelector{Provider: req.Provider, Model: req.Model}.QualifiedModel()
		if reason := chatUnsupportedReason(g.Router, model); reason != "" {
			return nil, core.NewInvalidRequestError(fmt.Sprintf(
				"model %q %s; plugin inference sends chat completions, so choose a chat model", model, reason), nil).WithParam("model")
		}
	}
	return g.Router.ChatCompletion(ctx, req)
}

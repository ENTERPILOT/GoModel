// Package builtin lists the plugins compiled into GoModel.
package builtin

import (
	"github.com/enterpilot/gomodel/internal/plugins/builtin/headeredit"
	"github.com/enterpilot/gomodel/internal/plugins/builtin/llmaltering"
	"github.com/enterpilot/gomodel/internal/plugins/builtin/llmjudge"
	"github.com/enterpilot/gomodel/internal/plugins/builtin/routeexample"
	"github.com/enterpilot/gomodel/internal/plugins/builtin/stringreplace"
	"github.com/enterpilot/gomodel/internal/plugins/builtin/systemprompt"
	"github.com/enterpilot/gomodel/pluginapi"
)

// All returns a factory per built-in plugin, in registration order.
func All() []func() pluginapi.Plugin {
	return []func() pluginapi.Plugin{
		systemprompt.New,
		llmaltering.New,
		stringreplace.New,
		headeredit.New,
		llmjudge.New,
		routeexample.New,
	}
}

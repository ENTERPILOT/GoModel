package plugins

import (
	"fmt"

	"github.com/enterpilot/gomodel/pluginapi"
)

// PluginError reports a fail-closed instance failure (error, panic, or
// timeout). The server renders it as HTTP 500 with code plugin_failure and
// records the instance name in the audit trail only.
type PluginError struct {
	Instance string
	Phase    pluginapi.Kind
	Err      error
}

func (e *PluginError) Error() string {
	return fmt.Sprintf("plugin instance %q failed in %s phase: %v", e.Instance, e.Phase, e.Err)
}

func (e *PluginError) Unwrap() error {
	return e.Err
}

// ShortCircuit is returned through the request path when a prompt-phase
// plugin answers the request itself (ActionRespond). The server renders
// Completion in the request's dialect with HTTP 200.
type ShortCircuit struct {
	Instance   string
	Decision   pluginapi.Decision
	Completion *pluginapi.Completion
}

func (e *ShortCircuit) Error() string {
	return fmt.Sprintf("plugin instance %q answered the request", e.Instance)
}

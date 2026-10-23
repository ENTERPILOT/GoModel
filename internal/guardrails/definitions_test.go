package guardrails

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/pluginapi"
)

func TestTypeFieldsFromSchemaKeepsRangeBounds(t *testing.T) {
	schema := []pluginapi.Field{
		{Key: "weight", Label: "Weight", Input: pluginapi.InputRange, Min: 0, Max: 1, Step: 0.05, Default: 0.35, Scope: pluginapi.ScopeRoute},
		{Key: "window", Label: "Window", Input: pluginapi.InputText, Scope: pluginapi.ScopeRoute},
	}

	fields := TypeFieldsFromSchema(schema, pluginapi.ScopeRoute)
	require.Len(t, fields, 2)

	require.Equal(t, "range", fields[0].Input)
	require.Equal(t, 1.0, fields[0].Max, "a range keeps its upper bound for the dashboard")
	require.Equal(t, 0.05, fields[0].Step, "a range keeps its step for the dashboard")
	require.Equal(t, 0.35, fields[0].Default)
	require.Zero(t, fields[1].Max, "a non-range field carries no bounds")
}

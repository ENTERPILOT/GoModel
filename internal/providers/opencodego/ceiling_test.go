package opencodego

import (
	"context"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// The effort this adapter injects or rounds to stays within the caller's
// effort ceiling.
func TestWithinEffortCeiling(t *testing.T) {
	tests := []struct {
		name          string
		reasoning     *core.Reasoning
		flat          string
		defaultEffort string
		ceiling       string
		// want is the effort sent upstream; "" means the request is kept.
		want string
	}{
		{name: "no ceiling keeps the default", defaultEffort: "max"},
		{name: "a default above the ceiling is lowered", defaultEffort: "max", ceiling: "high", want: "high"},
		{name: "a default within the ceiling is kept", defaultEffort: "low", ceiling: "medium"},
		{name: "a ceiling below every level sends the lowest", defaultEffort: "high", ceiling: "none", want: "low"},
		{name: "no injection, nothing to bound", ceiling: "low"},
		{name: "xhigh rounds to max upstream, so it is lowered", reasoning: &core.Reasoning{Effort: "xhigh"}, defaultEffort: "low", ceiling: "xhigh", want: "high"},
		{name: "a flat effort above the ceiling is replaced", flat: `"max"`, defaultEffort: "low", ceiling: "high", want: "high"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &core.ChatRequest{Model: "kimi-k2-thinking", Reasoning: tt.reasoning}
			if tt.flat != "" {
				req.ExtraFields = core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"reasoning_effort": json.RawMessage(tt.flat)})
			}
			ctx := core.WithRequestCeilings(context.Background(), core.RequestCeilings{ReasoningEffort: tt.ceiling})

			got := withinEffortCeiling(ctx, req, tt.defaultEffort)

			if tt.want == "" {
				assert.Same(t, req, got)
				return
			}
			require.NotNil(t, got.Reasoning)
			assert.Equal(t, tt.want, got.Reasoning.Effort)
			assert.Nil(t, got.ExtraFields.Lookup("reasoning_effort"))
		})
	}
}

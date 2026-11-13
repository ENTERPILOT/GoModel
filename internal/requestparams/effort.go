package requestparams

import (
	"fmt"
	"slices"
	"strings"

	"github.com/enterpilot/gomodel/internal/validation"
)

// reasoningEffortLevels is the gateway's common reasoning effort scale, in
// ascending order. "none" turns reasoning off where the provider allows it.
// Provider adapters translate a level to what each model accepts.
var reasoningEffortLevels = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// ReasoningEffortLevels returns the common reasoning effort scale, lowest
// first. The result is the caller's own copy, so the scale validation and
// ranking depend on cannot be changed from outside.
func ReasoningEffortLevels() []string {
	return slices.Clone(reasoningEffortLevels)
}

// NormalizeReasoningEffort trims and lower-cases a configured effort,
// rejecting values outside ReasoningEffortLevels. Empty stays empty. field
// names the setting in the message.
func NormalizeReasoningEffort(field, effort string) (string, error) {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" || slices.Contains(reasoningEffortLevels, effort) {
		return effort, nil
	}
	return "", validation.NewError(fmt.Sprintf("unknown %s %q (use one of: %s)", field, effort, strings.Join(reasoningEffortLevels, ", ")), nil)
}

// EffortRank returns the position of effort on ReasoningEffortLevels, or -1
// for a value the scale does not know. Spelling is matched as clients send
// it: trimmed and case-insensitive.
func EffortRank(effort string) int {
	return slices.Index(reasoningEffortLevels, strings.ToLower(strings.TrimSpace(effort)))
}

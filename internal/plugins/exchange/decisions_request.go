package exchange

import (
	"fmt"
	"sort"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"
)

// FromDecisionRequest builds the unified prompt for an OpenAI Decisions
// request. Its input is a subset of a Responses input (a string, or user
// messages of input_text and input_image parts), so it maps the same way: a
// string input is message "m0", messages are "m<index>". The questions are
// the application's fixed schema and are not exposed.
func FromDecisionRequest(req *core.DecisionRequest) (*pluginapi.Prompt, error) {
	responses, err := decisionAsResponses(req)
	if err != nil {
		return nil, err
	}
	return FromResponsesRequest(responses)
}

// ApplyToDecisionRequest returns a copy of original with the prompt's edits
// to the input applied; untouched items keep their exact JSON, so members the
// mapper does not model (prompt_cache_breakpoint) survive. Removing the whole
// input is an error, since the request would have nothing to decide on.
// Edits a decision request has no place for are not applied: messages
// inserted with a role other than user (the input holds user messages only)
// and parameter changes. DecisionUncarriedEdits lists them.
func ApplyToDecisionRequest(original *core.DecisionRequest, p *pluginapi.Prompt) (*core.DecisionRequest, error) {
	if original == nil || p == nil {
		return nil, fmt.Errorf("exchange: nil decision request or prompt")
	}
	result := *original
	changes := p.Changes()
	if len(changes.Messages) == 0 {
		return &result, nil
	}
	carried := p.Clone()
	carried.Messages = carried.Messages[:0]
	for _, m := range p.Messages {
		if changes.Messages[m.ID] == pluginapi.ChangeInserted && m.Role != pluginapi.RoleUser {
			continue
		}
		carried.Messages = append(carried.Messages, m)
	}
	if len(carried.Messages) == 0 {
		return nil, fmt.Errorf("exchange: the decision input was removed")
	}

	responses, err := decisionAsResponses(original)
	if err != nil {
		return nil, err
	}
	applied, err := ApplyToResponsesRequest(responses, carried)
	if err != nil {
		return nil, err
	}
	input, err := json.Marshal(applied.Input)
	if err != nil {
		return nil, fmt.Errorf("exchange: encode decision input: %w", err)
	}
	result.Input = input
	return &result, nil
}

// DecisionUncarriedEdits describes the kinds of prompt edits
// ApplyToDecisionRequest does not apply ("inserted system message",
// parameter "temperature"), deduplicated and sorted, or nil when every edit
// was carried.
func DecisionUncarriedEdits(p *pluginapi.Prompt) []string {
	if p == nil {
		return nil
	}
	changes := p.Changes()
	seen := map[string]struct{}{}
	for _, m := range p.Messages {
		if changes.Messages[m.ID] == pluginapi.ChangeInserted && m.Role != pluginapi.RoleUser {
			seen[fmt.Sprintf("inserted %s message", m.Role)] = struct{}{}
		}
	}
	for name := range changes.Params {
		seen[fmt.Sprintf("parameter %q", name)] = struct{}{}
	}
	if len(seen) == 0 {
		return nil
	}
	uncarried := make([]string, 0, len(seen))
	for description := range seen {
		uncarried = append(uncarried, description)
	}
	sort.Strings(uncarried)
	return uncarried
}

// decisionAsResponses reads a decision request as the Responses request with
// the same model and input, decoding the input the way a Responses input is
// decoded.
func decisionAsResponses(req *core.DecisionRequest) (*core.ResponsesRequest, error) {
	if req == nil {
		return nil, fmt.Errorf("exchange: nil decision request")
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("exchange: encode decision request: %w", err)
	}
	var responses core.ResponsesRequest
	if err := json.Unmarshal(raw, &responses); err != nil {
		return nil, fmt.Errorf("exchange: decode decision input: %w", err)
	}
	return &responses, nil
}

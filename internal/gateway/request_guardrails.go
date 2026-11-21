package gateway

import "github.com/enterpilot/gomodel/internal/core"

// SelectRequestGuardrails adds the guardrails a request named to its
// workflow. A request that names a guardrail fails when it cannot run: the
// resolver cannot select guardrails, or the matched workflow does not offer
// it. Skipping a check the client asked for is never the fallback.
func SelectRequestGuardrails(resolver WorkflowPolicyResolver, workflow *core.Workflow, names []string) error {
	if len(names) == 0 {
		return nil
	}
	selector, ok := resolver.(RequestGuardrailSelector)
	if !ok || workflow == nil {
		return core.NewRequestGuardrailUnavailableError(names[0])
	}
	return selector.SelectRequestGuardrails(workflow, names)
}

// TakeChatRequestGuardrails removes the guardrails field from a chat request
// and returns the names it held.
func TakeChatRequestGuardrails(req *core.ChatRequest) ([]string, error) {
	if req == nil {
		return nil, nil
	}
	names, rest, err := core.TakeRequestGuardrails(req.ExtraFields)
	if err != nil {
		return nil, err
	}
	req.ExtraFields = rest
	return names, nil
}

// TakeResponsesRequestGuardrails removes the guardrails field from a
// Responses request and returns the names it held.
func TakeResponsesRequestGuardrails(req *core.ResponsesRequest) ([]string, error) {
	if req == nil {
		return nil, nil
	}
	names, rest, err := core.TakeRequestGuardrails(req.ExtraFields)
	if err != nil {
		return nil, err
	}
	req.ExtraFields = rest
	return names, nil
}

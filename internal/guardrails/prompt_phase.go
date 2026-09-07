package guardrails

import (
	"context"
	"errors"
	"log/slog"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

// promptRun executes one prompt chain over a unified prompt and records the
// outcome in the request state. It returns a gateway error for block and
// fail-closed failures, a *plugins.ShortCircuit for respond, and reports
// whether the prompt was edited.
type promptRun struct {
	chain *plugins.Chain
	state *plugins.RequestState
	meta  pluginapi.Meta
}

func newPromptRun(ctx context.Context, chain *plugins.Chain) promptRun {
	return promptRun{chain: chain, state: plugins.RequestStateFor(ctx), meta: plugins.MetaFromContext(ctx, core.GetWorkflow(ctx))}
}

func (r promptRun) run(ctx context.Context, prompt *pluginapi.Prompt) (edited bool, err error) {
	x := r.state.NewExchange(ctx, r.meta)
	x.Prompt = prompt
	outcome, runErr := r.chain.RunPrompt(ctx, x)
	r.state.Finish(x)
	edited = prompt.Changes().Dirty
	records := make([]plugins.DecisionRecord, 0, len(outcome.Records))
	for _, record := range outcome.Records {
		records = append(records, plugins.DecisionRecord{
			Phase:    pluginapi.KindPrompt,
			Instance: record.Instance,
			Decision: record.Decision,
			Err:      record.Err,
			Edited:   edited && mutatesInstance(r.chain, record.Instance),
		})
	}
	r.state.Record(records...)

	if runErr != nil {
		if pluginErr, ok := errors.AsType[*plugins.PluginError](runErr); ok {
			slog.Warn("guardrail plugin failed closed", "request_id", r.meta.RequestID, "instance", pluginErr.Instance, "phase", "prompt", "error", pluginErr.Err)
			return edited, plugins.FailureError(runErr)
		}
		return edited, runErr
	}
	switch outcome.Decision.Action {
	case pluginapi.ActionBlock:
		slog.Info("guardrail blocked request", "request_id", r.meta.RequestID, "instance", outcome.Instance, "code", outcome.Decision.Code)
		return edited, plugins.BlockError(outcome.Decision, plugins.DefaultBlockStatus(pluginapi.KindPrompt))
	case pluginapi.ActionRespond:
		slog.Info("guardrail answered request", "request_id", r.meta.RequestID, "instance", outcome.Instance, "code", outcome.Decision.Code)
		return edited, &plugins.ShortCircuit{Instance: outcome.Instance, Decision: outcome.Decision, Completion: outcome.Decision.Response}
	case pluginapi.ActionWarn:
		r.state.AddResponseHeader(plugins.GuardrailHeader, plugins.WarnHeaderValue(outcome.Decision))
	}
	return edited, nil
}

func mutatesInstance(chain *plugins.Chain, name string) bool {
	for _, inst := range chain.Instances() {
		if inst.Name == name {
			return inst.Mutates()
		}
	}
	return false
}

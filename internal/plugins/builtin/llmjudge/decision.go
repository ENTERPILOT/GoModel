package llmjudge

import (
	"context"
	"fmt"

	"github.com/enterpilot/gomodel/pluginapi"
)

// questionName names the decision question in the request and the answer.
const questionName = "violates_policy"

// decisions returns what asks the judge model through the Decisions API, or
// nil when the judge is asked by chat: engine chat, or engine auto for a
// model the gateway does not know as a decision model.
func (p *Plugin) decisions() pluginapi.Decisions {
	if p.engine == EngineChat {
		return nil
	}
	host, ok := p.host.(pluginapi.DecisionsHost)
	if !ok {
		return nil
	}
	decisions := host.Decisions()
	if p.engine == EngineDecision || decisions.DecisionModel(p.model) {
		return decisions
	}
	return nil
}

// askDecision asks the decision question about in and turns the probability
// of yes into a verdict: block at or above the threshold, allow below it. A
// refusal, or an answer of another type, is unclear.
func (p *Plugin) askDecision(ctx context.Context, decisions pluginapi.Decisions, in judged) (verdict, error) {
	var parts []pluginapi.Part
	if in.text != "" {
		parts = append(parts, pluginapi.Part{Kind: pluginapi.PartText, Text: in.text})
	}
	for _, url := range in.images {
		parts = append(parts, pluginapi.Part{Kind: pluginapi.PartImage, URL: url})
	}
	result, err := decisions.Decide(ctx, pluginapi.DecisionRequest{
		Model:     p.model,
		UserPath:  p.userPath,
		Input:     []pluginapi.Message{{Role: pluginapi.RoleUser, Parts: parts}},
		Questions: []pluginapi.Question{{Type: pluginapi.QuestionPredicate, Name: questionName, Instructions: p.question}},
	})
	if err != nil {
		return verdict{}, fmt.Errorf("%s: judge call failed: %w", Name, err)
	}
	if result == nil || len(result.Answers) == 0 {
		return verdict{Verdict: VerdictUnclear, Reason: "decision model returned no answer", NoVerdict: true}, nil
	}
	answer := result.Answers[0]
	switch {
	case answer.Refused():
		return verdict{Verdict: VerdictUnclear, Reason: "decision model declined to answer"}, nil
	case answer.Type != string(pluginapi.QuestionPredicate):
		return verdict{Verdict: VerdictUnclear, Reason: "decision model answered with a " + answer.Type + ", not a probability"}, nil
	}
	probability := answer.Probability
	p.host.Metrics().Observe("decision_probability", probability, nil)
	v := verdict{Verdict: VerdictAllow, Probability: &probability,
		Reason: fmt.Sprintf("violation probability %.2f is below the block threshold %.2f", probability, p.threshold)}
	if probability >= p.threshold {
		v.Verdict = VerdictBlock
		v.Reason = fmt.Sprintf("violation probability %.2f is at or above the block threshold %.2f", probability, p.threshold)
	}
	return v, nil
}

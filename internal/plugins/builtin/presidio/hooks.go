package presidio

import (
	"context"
	"encoding/json"
	"maps"
	"strings"
	"sync"

	"github.com/enterpilot/gomodel/pluginapi"
)

// Decision codes recorded in the audit trail.
const (
	// Code marks a detection when the action is block, respond, or warn.
	Code = "presidio_pii"
	// CodeBlocked marks a detection of a block_entities type.
	CodeBlocked = "presidio_blocked_entity"
)

// mappingKey is the Exchange.Values key of the request's placeholder table.
// It is shared by every presidio instance of the request, so one instance
// can anonymize in the prompt phase and another restore in the response
// phase.
const mappingKey = Name + ":mapping"

const maxConcurrentAnalyses = 8

// unit identifies the message or choice a piece of content belongs to.
type unit struct {
	message string
	choice  int
}

// job is one piece of content to analyze: a text part (one input) or a
// tool call's arguments (one input per string value). apply writes the
// outputs back.
type job struct {
	unit    unit
	inputs  []string
	outputs []string
	apply   func(outputs []string) error
}

// report accumulates what a phase found.
type report struct {
	mu           sync.Mutex
	entities     map[string]int
	units        map[unit]bool
	blocked      string
	replacements int
	restored     int
}

func newReport() *report {
	return &report{entities: map[string]int{}, units: map[unit]bool{}}
}

func (r *report) found() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entities) > 0
}

func (r *report) detail() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := map[string]any{}
	if len(r.entities) > 0 {
		entities := make(map[string]int, len(r.entities))
		maps.Copy(entities, r.entities)
		d["entities"] = entities
		d["messages"] = len(r.units)
	}
	if r.blocked != "" {
		d["blocked_entity"] = r.blocked
	}
	if r.replacements > 0 {
		d["replacements"] = r.replacements
	}
	if r.restored > 0 {
		d["restored"] = r.restored
	}
	return d
}

// pass is what one phase does with the content it analyzes.
type pass struct {
	fromPrompt bool // placeholders allocated here are restorable
	restore    bool // put restorable placeholders back
	requestID  string
}

// OnPrompt analyzes the text of the prompt messages of the configured
// roles, tool-result text and tool-call arguments included.
func (p *Plugin) OnPrompt(ctx context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	if x == nil || x.Prompt == nil {
		return pluginapi.Allow(), nil
	}
	var jobs []job
	for _, t := range x.Prompt.TextTargets() {
		if p.roles[t.Role] {
			jobs = append(jobs, textJob(t, x.Prompt.SetTargetText))
		}
	}
	if p.roles[pluginapi.RoleAssistant] {
		for _, ref := range x.Prompt.ToolCalls() {
			msgID, callID := ref.MessageID, ref.Call.ID
			if j, ok := argsJob(unit{message: msgID}, ref.Call.Arguments, func(args json.RawMessage) error {
				return x.Prompt.SetToolArguments(msgID, callID, args)
			}); ok {
				jobs = append(jobs, j)
			}
		}
	}
	rep := newReport()
	m := p.mapping(x)
	if err := p.run(ctx, jobs, m, rep, pass{fromPrompt: true, requestID: x.Meta.RequestID}); err != nil {
		return pluginapi.Decision{}, err
	}
	d := p.decide(rep)
	if p.restore && !d.Blocks() && m.hasRestorable() {
		d.NoStore = true
	}
	return d, nil
}

// OnResponse analyzes the assistant text and tool-call arguments of every
// choice and puts restorable values back.
func (p *Plugin) OnResponse(ctx context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	if x == nil || x.Response == nil {
		return pluginapi.Allow(), nil
	}
	var jobs []job
	for _, t := range x.Response.TextTargets() {
		jobs = append(jobs, textJob(t, x.Response.SetTargetText))
	}
	for i, choice := range x.Response.Choices {
		for _, part := range choice.Message.Parts {
			if part.Kind != pluginapi.PartToolCall || part.ToolCall == nil {
				continue
			}
			callID := part.ToolCall.ID
			if j, ok := argsJob(unit{choice: i}, part.ToolCall.Arguments, func(args json.RawMessage) error {
				return x.Response.SetToolArguments(i, callID, args)
			}); ok {
				jobs = append(jobs, j)
			}
		}
	}
	rep := newReport()
	if err := p.run(ctx, jobs, p.mapping(x), rep, pass{restore: p.restore, requestID: x.Meta.RequestID}); err != nil {
		return pluginapi.Decision{}, err
	}
	return p.decide(rep), nil
}

func textJob(t pluginapi.TextTarget, set func(pluginapi.TextTarget, string) error) job {
	return job{
		unit:   unit{message: t.MessageID, choice: t.Choice},
		inputs: []string{t.Text},
		apply:  func(out []string) error { return set(t, out[0]) },
	}
}

func argsJob(u unit, args json.RawMessage, set func(json.RawMessage) error) (job, bool) {
	tree, inputs, ok := argStrings(args)
	if !ok || len(inputs) == 0 {
		return job{}, false
	}
	return job{
		unit:   u,
		inputs: inputs,
		apply: func(out []string) error {
			encoded, err := withArgStrings(tree, out)
			if err != nil {
				return err
			}
			return set(encoded)
		},
	}, true
}

// run analyzes every job (at most 8 analyzer calls in flight), records the
// findings in rep, and writes the rewritten content back when the action
// edits or values are restored. A failed analyzer call fails the phase, so
// fail_mode decides; nothing is written back then.
func (p *Plugin) run(ctx context.Context, jobs []job, m *mapping, rep *report, ps pass) error {
	if len(jobs) == 0 {
		return nil
	}
	errs := make([]error, len(jobs))
	sem := make(chan struct{}, maxConcurrentAnalyses)
	var wg sync.WaitGroup
	for i := range jobs {
		wg.Add(1)
		go func(j *job, i int) {
			defer wg.Done()
			j.outputs = make([]string, len(j.inputs))
			for k, text := range j.inputs {
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					errs[i] = ctx.Err()
					return
				}
				out, err := p.process(ctx, text, 0, j.unit, m, rep, ps)
				<-sem
				if err != nil {
					errs[i] = err
					return
				}
				j.outputs[k] = out
			}
		}(&jobs[i], i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	if rep.blocked != "" || (p.action != ActionAnonymize && !ps.restore) {
		return nil
	}
	for i := range jobs {
		j := &jobs[i]
		changed := false
		for k := range j.inputs {
			if j.inputs[k] != j.outputs[k] {
				changed = true
				break
			}
		}
		if !changed {
			continue
		}
		if err := j.apply(j.outputs); err != nil {
			return err
		}
	}
	return nil
}

// process analyzes one string and returns it rewritten: anonymized when the
// action is anonymize, then with restorable values put back when restoring.
// Entities ending within the first skip bytes were seen in an earlier
// stream event and are left alone.
func (p *Plugin) process(ctx context.Context, text string, skip int, u unit, m *mapping, rep *report, ps pass) (string, error) {
	out := text
	if strings.TrimSpace(text) != "" {
		results, err := p.client.analyze(ctx, text, ps.requestID)
		if err != nil {
			return "", err
		}
		var spans []span
		for _, s := range byteSpans(text, results) {
			if s.end > skip {
				spans = append(spans, s)
			}
		}
		if len(spans) > 0 {
			rep.record(u, spans, p.blockEntities)
			if p.action == ActionAnonymize {
				out = rewrite(text, spans, func(s span, value string) string {
					if p.operator == OperatorReplace {
						return m.placeholder(s.entity, value, ps.fromPrompt)
					}
					return staticReplacement(p.operator, value)
				})
				rep.add(len(spans), 0)
			}
		}
	}
	if ps.restore {
		var n int
		out, n = m.restore(out)
		rep.add(0, n)
	}
	return out, nil
}

func (r *report) record(u unit, spans []span, blocking map[string]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.units[u] = true
	for _, s := range spans {
		r.entities[s.entity]++
		if blocking[s.entity] && r.blocked == "" {
			r.blocked = s.entity
		}
	}
}

func (r *report) add(replacements, restored int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.replacements += replacements
	r.restored += restored
}

// mapping returns the request's placeholder table, creating it on first
// use. Without a Values bag (a host that runs hooks bare) a throwaway
// table is used.
func (p *Plugin) mapping(x *pluginapi.Exchange) *mapping {
	if x.Values == nil {
		return newMapping()
	}
	if v, ok := x.Values.Get(mappingKey); ok {
		if m, ok := v.(*mapping); ok {
			return m
		}
	}
	m := newMapping()
	x.Values.Set(mappingKey, m)
	return m
}

// decide turns the findings into the configured decision.
func (p *Plugin) decide(rep *report) pluginapi.Decision {
	detail := rep.detail()
	if rep.blocked != "" {
		return p.enforce(CodeBlocked, detail)
	}
	if rep.found() {
		switch p.action {
		case ActionBlock, ActionRespond:
			return p.enforce(Code, detail)
		case ActionWarn:
			return pluginapi.Warn(Code, p.message, detail)
		}
	}
	if len(detail) == 0 {
		return pluginapi.Allow()
	}
	return pluginapi.Decision{Action: pluginapi.ActionAllow, Detail: detail}
}

// enforce renders a detection as the blocking action: respond when that is
// the configured action, block otherwise.
func (p *Plugin) enforce(code string, detail map[string]any) pluginapi.Decision {
	if p.action == ActionRespond {
		d := pluginapi.Respond(p.message)
		d.Code = code
		d.Detail = detail
		return d
	}
	d := pluginapi.Block(p.blockStatus, code, p.message)
	d.Detail = detail
	return d
}

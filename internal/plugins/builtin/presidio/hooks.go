package presidio

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
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
	unit       unit
	inputs     []string
	spans      [][]span
	outputs    []string
	restorable bool
	apply      func(outputs []string) error
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
	// prompt marks the prompt phase: placeholders allocated there for user,
	// assistant, and tool content are restorable when restore is on.
	// System and developer values are anonymized but never restored, so a
	// model that repeats their placeholder cannot disclose them.
	prompt    bool
	restore   bool // put restorable placeholders back
	requestID string
}

// OnPrompt analyzes the text of the prompt messages of the configured
// roles, tool-result text and tool-call arguments included.
func (p *Plugin) OnPrompt(ctx context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	if x == nil || x.Prompt == nil {
		return pluginapi.Allow(), nil
	}
	m := p.mapping(x)
	var jobs []job
	for _, t := range x.Prompt.TextTargets() {
		if p.roles[t.Role] {
			jobs = append(jobs, textJob(t, p.restorable(t.Role), x.Prompt.SetTargetText))
		} else {
			// Not analyzed, but the model reads it: its placeholder-shaped
			// text must not collide with a new placeholder either.
			m.reserve(t.Text)
		}
	}
	if p.roles[pluginapi.RoleAssistant] {
		for _, ref := range x.Prompt.ToolCalls() {
			msgID, callID := ref.MessageID, ref.Call.ID
			if j, ok := argsJob(unit{message: msgID}, ref.Call.Arguments, p.restorable(pluginapi.RoleAssistant), func(args json.RawMessage) error {
				return x.Prompt.SetToolArguments(msgID, callID, args)
			}); ok {
				jobs = append(jobs, j)
			}
		}
	}
	rep := newReport()
	if err := p.run(ctx, jobs, m, rep, pass{prompt: true, requestID: x.Meta.RequestID}); err != nil {
		return pluginapi.Decision{}, err
	}
	d := p.decide(rep)
	if !d.Blocks() && m.hasRestorable() {
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
		jobs = append(jobs, textJob(t, false, x.Response.SetTargetText))
	}
	for i, choice := range x.Response.Choices {
		for _, part := range choice.Message.Parts {
			if part.Kind != pluginapi.PartToolCall || part.ToolCall == nil {
				continue
			}
			callID := part.ToolCall.ID
			if j, ok := argsJob(unit{choice: i}, part.ToolCall.Arguments, false, func(args json.RawMessage) error {
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

// restorable reports whether values of a prompt role may be put back into
// the response: never for system and developer messages, and only when
// this instance restores.
func (p *Plugin) restorable(role pluginapi.Role) bool {
	return p.restore && role != pluginapi.RoleSystem && role != pluginapi.RoleDeveloper
}

func textJob(t pluginapi.TextTarget, restorable bool, set func(pluginapi.TextTarget, string) error) job {
	return job{
		unit:       unit{message: t.MessageID, choice: t.Choice},
		inputs:     []string{t.Text},
		restorable: restorable,
		apply:      func(out []string) error { return set(t, out[0]) },
	}
}

func argsJob(u unit, args json.RawMessage, restorable bool, set func(json.RawMessage) error) (job, bool) {
	tree, inputs, ok := argStrings(args)
	if !ok || len(inputs) == 0 {
		return job{}, false
	}
	return job{
		unit:       u,
		inputs:     inputs,
		restorable: restorable,
		apply: func(out []string) error {
			encoded, err := withArgStrings(tree, out)
			if err != nil {
				return err
			}
			return set(encoded)
		},
	}, true
}

// run analyzes every input (at most 8 analyzer calls in flight), then
// rewrites them in document order and writes the results back when the
// action edits or values are restored. Placeholder-shaped text already in
// any input is reserved before the first placeholder is allocated. Nothing is
// recorded or written, and no placeholder allocated, until every analyzer
// call has succeeded: a failed call fails the phase, so fail_mode decides.
func (p *Plugin) run(ctx context.Context, jobs []job, m *mapping, rep *report, ps pass) error {
	if len(jobs) == 0 {
		return nil
	}
	if err := p.analyzeAll(ctx, jobs, ps.requestID); err != nil {
		return err
	}
	for i := range jobs {
		for _, text := range jobs[i].inputs {
			m.reserve(text)
		}
	}
	for i := range jobs {
		j := &jobs[i]
		j.outputs = make([]string, len(j.inputs))
		for k, text := range j.inputs {
			j.outputs[k] = p.rewriteOne(text, j.spans[k], j.unit, j.restorable, m, rep, ps)
		}
	}
	if rep.blocked != "" || (p.action != ActionAnonymize && !ps.restore) {
		return nil
	}
	for i := range jobs {
		j := &jobs[i]
		if slices.Equal(j.inputs, j.outputs) {
			continue
		}
		if err := j.apply(j.outputs); err != nil {
			return err
		}
	}
	return nil
}

// analyzeAll fills j.spans for every job concurrently.
func (p *Plugin) analyzeAll(ctx context.Context, jobs []job, requestID string) error {
	errs := make([]error, len(jobs))
	sem := make(chan struct{}, maxConcurrentAnalyses)
	var wg sync.WaitGroup
	for i := range jobs {
		wg.Add(1)
		go func(j *job, i int) {
			defer wg.Done()
			j.spans = make([][]span, len(j.inputs))
			for k, text := range j.inputs {
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					errs[i] = ctx.Err()
					return
				}
				spans, err := p.analyze(ctx, text, 0, requestID)
				<-sem
				if err != nil {
					errs[i] = err
					return
				}
				j.spans[k] = spans
			}
		}(&jobs[i], i)
	}
	wg.Wait()
	return errors.Join(errs...)
}

// analyze returns the entities of text as byte spans, leaving out those
// ending within the first skip bytes (seen in an earlier stream event).
// Blank text is not sent: the analyzer rejects it.
func (p *Plugin) analyze(ctx context.Context, text string, skip int, requestID string) ([]span, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	results, err := p.client.analyze(ctx, text, requestID)
	if err != nil {
		return nil, err
	}
	var spans []span
	for _, s := range byteSpans(text, results) {
		if s.end > skip {
			spans = append(spans, s)
		}
	}
	return spans, nil
}

// rewriteOne records the spans of one string and returns it rewritten:
// anonymized when the action is anonymize, then with restorable values put
// back when restoring.
func (p *Plugin) rewriteOne(text string, spans []span, u unit, restorable bool, m *mapping, rep *report, ps pass) string {
	out := text
	if len(spans) > 0 {
		rep.record(u, spans, p.blockEntities)
		if p.action == ActionAnonymize {
			out = rewrite(text, spans, func(s span, value string) string {
				if p.operator == OperatorReplace {
					return m.placeholder(s.entity, value, ps.prompt && restorable)
				}
				return staticReplacement(p.operator, value)
			})
			rep.add(len(spans), 0)
		}
	}
	if ps.restore {
		var n int
		out, n = m.restore(out)
		rep.add(0, n)
	}
	return out
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

// decide turns the findings into the configured decision. A response
// with restored values is kept out of the response cache.
func (p *Plugin) decide(rep *report) pluginapi.Decision {
	detail := rep.detail()
	var d pluginapi.Decision
	switch {
	case rep.blocked != "":
		d = p.enforcement.Reject(CodeBlocked, detail)
	case rep.found() && p.action != ActionAnonymize:
		d = p.enforcement.Enforce(Code, detail)
	case len(detail) == 0:
		return pluginapi.Allow()
	default:
		d = pluginapi.Decision{Action: pluginapi.ActionAllow, Detail: detail}
	}
	d.NoStore = rep.restored > 0
	return d
}

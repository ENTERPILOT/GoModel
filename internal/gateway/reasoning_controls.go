package gateway

import (
	"bytes"
	"slices"
	"strings"
	"unicode"

	"github.com/goccy/go-json"
	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/core"
)

// The common reasoning fields (reasoning.effort and the flat
// reasoning_effort) are not the only way a request turns reasoning on: the
// OpenAI-compatible adapters forward unknown members verbatim, Cohere
// forwards thinking, and Gemini reads extra_body.google.thinking_config. A
// forced effort or an effort ceiling has to see and replace these too, or
// the upstream would read them instead.
//
// reasoningControlFields are the provider-specific members, at the top level
// of the body or inside extra_body:
//   - thinking: {"type":"enabled"|"disabled", "budget_tokens"|"token_budget": N}
//     (Cohere, Xiaomi MiMo, DeepSeek, Kimi and GLM upstreams)
//   - enable_thinking and thinking_budget (Qwen, Bailian, vLLM/SGLang servers)
//   - think: a boolean or a level (Ollama)
var reasoningControlFields = []string{"thinking", "enable_thinking", "thinking_budget", "think"}

// chatTemplateKwargsField carries chat template switches to vLLM, SGLang and
// llama.cpp servers; templateControlFields are the ones that set reasoning.
const chatTemplateKwargsField = "chat_template_kwargs"

var templateControlFields = []string{"enable_thinking", "thinking", "reasoning_effort", "thinking_budget"}

// unrankedEffort stands for an effort the common scale cannot place, such as
// a token budget or a bare "on": it ranks as unknown, which a ceiling treats
// as exceeding, so the control is replaced by the ceiling, never forwarded.
const unrankedEffort = "?"

// requested is what a request's reasoning controls ask for.
type requested struct {
	// efforts are the efforts the request sets, as the client spelled them
	// or unrankedEffort.
	efforts []string
	// unsettledOff reports a control object that names its type more than
	// once and is "disabled" in the last of them, the one a decoder keeps,
	// but not in all. Such a control is replaced, so that no decoder reads
	// the other occurrence; it asks for no reasoning, though, so it is not
	// what the ceiling is written for (see effortWithinCeiling). It is a
	// flag of its own because no effort string could stand for it: a client
	// can send any of them.
	unsettledOff bool
}

// effortReader adds the effort one control's value sets to found.
type effortReader func(found *requested, raw json.RawMessage)

// controls names the members of one JSON object that set reasoning. Names
// are lower-case and matched in any letter case (see foldName): the
// gateway's decoder and Gemini's read a re-cased member as the member
// itself, and so does every upstream that matches names the way Go does. A
// repeated member counts once per occurrence, since decoders differ in which
// one they keep, or merge them.
type controls struct {
	// reader returns how to read the named member's effort, or nil when the
	// member sets none.
	reader func(name string) effortReader
	// objects are the members that hold further controls.
	objects map[string]*controls
}

var (
	// googleControls are extra_body.google.thinking_config's budget and level.
	googleControls = &controls{objects: map[string]*controls{
		"thinking_config": {reader: func(name string) effortReader {
			if isGoogleThinkingEffortMember(name) {
				return readControlEffort
			}
			return nil
		}},
	}}
	templateControls = &controls{reader: readers(readControlEffort, templateControlFields...)}

	// clientControls are every reasoning control a request carries next to
	// the typed reasoning.effort. A re-cased "reasoning" is one of them: the
	// decoder reads it into the typed field and keeps the member too.
	clientControls = &controls{
		reader: func(name string) effortReader {
			if name == "reasoning" {
				return readReasoningObjectEffort
			}
			return bodyControls(name)
		},
		objects: map[string]*controls{
			chatTemplateKwargsField: templateControls,
			"extra_body": {reader: bodyControls, objects: map[string]*controls{
				chatTemplateKwargsField: templateControls,
				"google":                googleControls,
			}},
		},
	}
	// effortSpellings are the controls an adapter reads in place of
	// reasoning.effort: the flat reasoning_effort and Gemini's budget and level.
	effortSpellings = &controls{
		reader: readers(readFlatEffort, flatReasoningEffortField),
		objects: map[string]*controls{
			"extra_body": {objects: map[string]*controls{"google": googleControls}},
		},
	}
)

// bodyControls reads the flat reasoning_effort and the provider-specific
// controls, at the top level of the body or inside extra_body.
func bodyControls(name string) effortReader {
	if name == flatReasoningEffortField {
		return readFlatEffort
	}
	return readers(readControlEffort, reasoningControlFields...)(name)
}

func readers(read effortReader, names ...string) func(string) effortReader {
	return func(name string) effortReader {
		if slices.Contains(names, name) {
			return read
		}
		return nil
	}
}

// without returns the rewrite that drops every control from one object, at
// any depth, and adds the effort each one set to found when it is not nil. An object the rewrite leaves empty is dropped; a member that should
// hold controls but is not an object carries none and stays.
func (c *controls) without(found *requested) func(string, json.RawMessage) json.RawMessage {
	return func(name string, value json.RawMessage) json.RawMessage {
		name = foldName(name)
		if c.reader != nil {
			if read := c.reader(name); read != nil {
				if found != nil {
					read(found, value)
				}
				return nil
			}
		}
		nested, ok := c.objects[name]
		if !ok {
			return value
		}
		rewritten, changed := core.RewriteJSONObject(value, nested.without(found))
		if changed && string(rewritten) == "{}" {
			return nil
		}
		return rewritten
	}
}

// foldName returns the lower-case name every spelling that
// strings.EqualFold matches to it shares, so the names Go's decoders take
// for one member compare equal.
func foldName(name string) string {
	return strings.ToLower(strings.Map(func(r rune) rune {
		lowest := r
		for folded := unicode.SimpleFold(r); folded != r; folded = unicode.SimpleFold(folded) {
			lowest = min(lowest, folded)
		}
		return lowest
	}, name))
}

// requestedEfforts lists every reasoning effort a request sets, in any
// spelling, letter case and occurrence of a repeated member.
func requestedEfforts(reasoning *core.Reasoning, fields core.UnknownJSONFields) requested {
	var found requested
	if reasoning != nil && strings.TrimSpace(reasoning.Effort) != "" {
		found.efforts = append(found.efforts, reasoning.Effort)
	}
	fields.Rewritten(clientControls.without(&found))
	return found
}

// readFlatEffort reads a flat reasoning_effort: a level, or a value the
// scale cannot place.
func readFlatEffort(found *requested, raw json.RawMessage) {
	if isSet(raw) {
		found.efforts = append(found.efforts, stringOrUnranked(raw))
	}
}

// readReasoningObjectEffort reads every effort member of a reasoning
// object.
func readReasoningObjectEffort(found *requested, raw json.RawMessage) {
	gjson.ParseBytes(raw).ForEach(func(key, value gjson.Result) bool {
		if strings.EqualFold(key.String(), "effort") {
			readFlatEffort(found, json.RawMessage(value.Raw))
		}
		return true
	})
}

// readControlEffort places one control value on the common scale: false,
// a zero budget and {"type":"disabled"} turn reasoning off, a string is a
// level, and anything else (a budget, true, "enabled") cannot be ranked.
func readControlEffort(found *requested, raw json.RawMessage) {
	raw = bytes.TrimSpace(raw)
	if !isSet(raw) {
		return
	}
	effort := unrankedEffort
	switch {
	case bytes.Equal(raw, []byte("false")), isZero(raw):
		effort = "none"
	case raw[0] == '"':
		effort = stringOrUnranked(raw)
	case raw[0] == '{':
		switch last, every := isDisabled(raw); {
		case every:
			effort = "none"
		case last:
			found.unsettledOff = true
			return
		}
	}
	found.efforts = append(found.efforts, effort)
}

// isDisabled reports whether a control object's type is "disabled" in the
// last occurrence of the member, in any letter case, which is the one a
// decoder that matches names as Go does keeps, and whether it is in every
// occurrence, so that no decoder reads anything else.
func isDisabled(object json.RawMessage) (last, every bool) {
	every = true
	gjson.ParseBytes(object).ForEach(func(key, value gjson.Result) bool {
		if strings.EqualFold(key.String(), "type") {
			last = value.Type == gjson.String && strings.EqualFold(strings.TrimSpace(value.String()), "disabled")
			every = every && last
		}
		return true
	})
	return last, every && last
}

// isZero reports whether raw is the number zero in any JSON spelling: 0, -0,
// 0.0, 0e0.
func isZero(raw json.RawMessage) bool {
	var number float64
	return (raw[0] == '-' || (raw[0] >= '0' && raw[0] <= '9')) && json.Unmarshal(raw, &number) == nil && number == 0
}

// withoutClientEffort drops the effort spellings a forced effort replaces
// once the parameter sets are applied: the flat reasoning_effort and Gemini's
// thinking budget and level, whoever set them, so no provider reads them
// instead of the forced reasoning.effort. The other provider controls stay:
// the client's reasoning controls are already gone (see
// withoutReasoningControls), so the ones left are those a virtual model's
// own params set next to its effort.
func withoutClientEffort(fields core.UnknownJSONFields) core.UnknownJSONFields {
	return fields.Rewritten(effortSpellings.without(nil))
}

// withoutReasoningControls drops every reasoning control a request carries,
// in any letter case and every occurrence: under an effort ceiling, where
// what is left upstream is the ceiling the gateway writes to
// reasoning.effort, and from the client's request before a forced effort and
// its parameter sets apply. Members that set no effort stay, the client's
// included: Gemini's include_thoughts, unrelated chat template switches.
func withoutReasoningControls(fields core.UnknownJSONFields) core.UnknownJSONFields {
	return fields.Rewritten(clientControls.without(nil))
}

// isGoogleThinkingEffortMember reports whether a thinking_config member sets
// the budget or level, in any spelling: thinking_budget, thinkingBudget,
// Thinking_Level, ...
func isGoogleThinkingEffortMember(key string) bool {
	folded := strings.ReplaceAll(strings.ToLower(key), "_", "")
	return folded == "thinkingbudget" || folded == "thinkinglevel"
}

func isSet(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && !bytes.Equal(raw, []byte("null"))
}

func stringOrUnranked(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value) == "" {
		return unrankedEffort
	}
	return value
}

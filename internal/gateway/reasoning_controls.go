package gateway

import (
	"bytes"
	"slices"
	"strings"

	"github.com/goccy/go-json"

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

// requestedEfforts lists every reasoning effort a request sets, in any
// spelling and any occurrence of a repeated member. Each is a level of the
// common scale or unrankedEffort.
func requestedEfforts(reasoning *core.Reasoning, fields core.UnknownJSONFields) []string {
	var efforts []string
	if reasoning != nil && strings.TrimSpace(reasoning.Effort) != "" {
		efforts = append(efforts, reasoning.Effort)
	}
	efforts = append(efforts, memberEfforts(fields.LookupAll)...)
	for _, raw := range fields.LookupAll("extra_body") {
		body, _ := objectOf(raw)
		efforts = append(efforts, memberEfforts(mapLookup(body))...)
		efforts = append(efforts, googleThinkingEfforts(body)...)
	}
	return efforts
}

// memberEfforts reads the flat reasoning_effort and every provider-specific
// control among one object's members.
func memberEfforts(lookup func(string) []json.RawMessage) []string {
	var efforts []string
	for _, raw := range lookup(flatReasoningEffortField) {
		if isSet(raw) {
			efforts = append(efforts, stringOrUnranked(raw))
		}
	}
	for _, field := range reasoningControlFields {
		for _, raw := range lookup(field) {
			efforts = appendControlEffort(efforts, raw)
		}
	}
	for _, raw := range lookup(chatTemplateKwargsField) {
		kwargs, _ := objectOf(raw)
		for _, field := range templateControlFields {
			efforts = appendControlEffort(efforts, kwargs[field])
		}
	}
	return efforts
}

// googleThinkingEfforts reads extra_body.google.thinking_config. Gemini
// decodes both object names case-insensitively, so they are matched that
// way, and every member that names a thinking budget or level counts.
func googleThinkingEfforts(body map[string]json.RawMessage) []string {
	var efforts []string
	for _, google := range foldedObjects(body, "google") {
		for _, thinking := range foldedObjects(google, "thinking_config") {
			for key, raw := range thinking {
				if isGoogleThinkingEffortMember(key) {
					efforts = appendControlEffort(efforts, raw)
				}
			}
		}
	}
	return efforts
}

// appendControlEffort places one control value on the common scale: false,
// a zero budget and {"type":"disabled"} turn reasoning off, a string is a
// level, and anything else (a budget, true, "enabled") cannot be ranked.
func appendControlEffort(efforts []string, raw json.RawMessage) []string {
	raw = bytes.TrimSpace(raw)
	if !isSet(raw) {
		return efforts
	}
	switch {
	case bytes.Equal(raw, []byte("false")), isZero(raw):
		return append(efforts, "none")
	case raw[0] == '"':
		return append(efforts, stringOrUnranked(raw))
	case raw[0] == '{':
		var object struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &object) == nil && strings.EqualFold(strings.TrimSpace(object.Type), "disabled") {
			return append(efforts, "none")
		}
	}
	return append(efforts, unrankedEffort)
}

// isZero reports whether raw is the number zero in any JSON spelling: 0, -0,
// 0.0, 0e0.
func isZero(raw json.RawMessage) bool {
	var number float64
	return (raw[0] == '-' || (raw[0] >= '0' && raw[0] <= '9')) && json.Unmarshal(raw, &number) == nil && number == 0
}

// withoutClientEffort drops the effort spellings a forced effort replaces
// once the parameter sets are applied: the flat reasoning_effort and Gemini's
// thinking budget and level, so no provider reads them instead of the forced
// reasoning.effort. Provider controls such as chat_template_kwargs stay:
// the client's are already gone (see withoutReasoningControls), so the ones
// left are those a virtual model's own params set next to its effort.
func withoutClientEffort(fields core.UnknownJSONFields) core.UnknownJSONFields {
	fields = fields.Without(flatReasoningEffortField)
	return rewriteMember(fields, "extra_body", withoutGoogleThinkingEffort)
}

// withoutReasoningControls drops every reasoning control a request carries:
// under an effort ceiling, where what is left upstream is the ceiling the
// gateway writes to reasoning.effort, and from the client's request before a
// forced effort and its parameter sets apply. Other members, such as Gemini's
// include_thoughts or unrelated chat template switches, stay.
func withoutReasoningControls(fields core.UnknownJSONFields) core.UnknownJSONFields {
	fields = fields.Without(append([]string{flatReasoningEffortField}, reasoningControlFields...)...)
	fields = rewriteMember(fields, chatTemplateKwargsField, withoutTemplateControls)
	return rewriteMember(fields, "extra_body", withoutExtraBodyControls)
}

func withoutTemplateControls(kwargs map[string]json.RawMessage) bool {
	return deleteMembers(kwargs, func(key string) bool { return slices.Contains(templateControlFields, key) })
}

func withoutExtraBodyControls(body map[string]json.RawMessage) bool {
	changed := deleteMembers(body, func(key string) bool {
		return key == flatReasoningEffortField || slices.Contains(reasoningControlFields, key)
	})
	changed = rewriteChildren(body, chatTemplateKwargsField, false, withoutTemplateControls) || changed
	return withoutGoogleThinkingEffort(body) || changed
}

// withoutGoogleThinkingEffort drops the budget and level from
// extra_body.google.thinking_config, matching names as Gemini decodes them.
func withoutGoogleThinkingEffort(body map[string]json.RawMessage) bool {
	return rewriteChildren(body, "google", true, func(google map[string]json.RawMessage) bool {
		return rewriteChildren(google, "thinking_config", true, func(thinking map[string]json.RawMessage) bool {
			return deleteMembers(thinking, isGoogleThinkingEffortMember)
		})
	})
}

// isGoogleThinkingEffortMember reports whether a thinking_config member sets
// the budget or level, in any spelling: thinking_budget, thinkingBudget,
// Thinking_Level, ...
func isGoogleThinkingEffortMember(key string) bool {
	folded := strings.ReplaceAll(strings.ToLower(key), "_", "")
	return folded == "thinkingbudget" || folded == "thinkinglevel"
}

// rewriteMember applies rewrite to the object stored under key. When it
// changes anything, or the member is repeated, every occurrence is replaced
// by the rewritten last one (the one most decoders keep), and an object left
// empty is dropped. A value that is not an object is left alone.
func rewriteMember(fields core.UnknownJSONFields, key string, rewrite func(map[string]json.RawMessage) bool) core.UnknownJSONFields {
	values := fields.LookupAll(key)
	if len(values) == 0 {
		return fields
	}
	changed := len(values) > 1
	for _, raw := range values[:len(values)-1] {
		object, _ := objectOf(raw)
		changed = rewrite(object) || changed
	}
	last, ok := objectOf(values[len(values)-1])
	if !ok {
		return fields
	}
	if !rewrite(last) && !changed {
		return fields
	}
	rest := fields.Without(key)
	if len(last) == 0 {
		return rest
	}
	encoded, err := json.Marshal(last)
	if err != nil {
		return fields
	}
	merged, err := core.MergeUnknownJSONFields(rest, map[string]json.RawMessage{key: encoded})
	if err != nil {
		return fields
	}
	return merged
}

// rewriteChildren applies rewrite to each object member of parent named name
// (case-insensitively when fold is set), storing the result back and
// dropping objects left empty. It reports whether anything changed.
func rewriteChildren(parent map[string]json.RawMessage, name string, fold bool, rewrite func(map[string]json.RawMessage) bool) bool {
	changed := false
	for key, raw := range parent {
		if key != name && (!fold || !strings.EqualFold(key, name)) {
			continue
		}
		child, ok := objectOf(raw)
		if !ok || !rewrite(child) {
			continue
		}
		changed = true
		if len(child) == 0 {
			delete(parent, key)
			continue
		}
		if encoded, err := json.Marshal(child); err == nil {
			parent[key] = encoded
		}
	}
	return changed
}

func deleteMembers(object map[string]json.RawMessage, match func(string) bool) bool {
	changed := false
	for key := range object {
		if match(key) {
			delete(object, key)
			changed = true
		}
	}
	return changed
}

// foldedObjects returns the object members of parent whose name matches name
// case-insensitively, as Go's JSON decoders match struct fields.
func foldedObjects(parent map[string]json.RawMessage, name string) []map[string]json.RawMessage {
	var objects []map[string]json.RawMessage
	for key, raw := range parent {
		if !strings.EqualFold(key, name) {
			continue
		}
		if object, ok := objectOf(raw); ok {
			objects = append(objects, object)
		}
	}
	return objects
}

func objectOf(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var object map[string]json.RawMessage
	if !isSet(raw) || json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, false
	}
	return object, true
}

func mapLookup(object map[string]json.RawMessage) func(string) []json.RawMessage {
	return func(key string) []json.RawMessage {
		if raw, ok := object[key]; ok {
			return []json.RawMessage{raw}
		}
		return nil
	}
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

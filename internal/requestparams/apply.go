package requestparams

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// ApplyChat returns req with defaults filled in and params forced. The
// request is never mutated: the result is req itself when both sets are
// empty and a shallow copy otherwise.
func ApplyChat(req *core.ChatRequest, defaults, params Set) (*core.ChatRequest, error) {
	if req == nil || (len(defaults) == 0 && len(params) == 0) {
		return req, nil
	}
	cloned := *req
	a := newApplier(chatTypedKeys, req.ExtraFields)
	apply := func(key string, raw json.RawMessage, force bool) error {
		switch key {
		case "temperature":
			return setPtr(&cloned.Temperature, raw, force)
		case "top_p":
			return setPtr(&cloned.TopP, raw, force)
		case "max_tokens", "max_output_tokens":
			// Chat has no max_output_tokens: it sets max_tokens.
			if a.keepTokenLimit(cloned.MaxTokens, force, chatTokenLimitExtras...) {
				return nil
			}
			a.stripExtras(force, chatTokenLimitExtras...)
			return setPtr(&cloned.MaxTokens, raw, force)
		case "max_completion_tokens":
			// Kept in the operator's spelling: a reasoning deployment the
			// adapter does not recognize by name still needs it.
			if a.keepTokenLimit(cloned.MaxTokens, force, chatTokenLimitExtras...) {
				return nil
			}
			if force {
				cloned.MaxTokens = nil
			}
			a.stripExtras(force, "max_output_tokens")
			return a.routeExtra(key, raw, force)
		case "parallel_tool_calls":
			return setPtr(&cloned.ParallelToolCalls, raw, force)
		case "user":
			return setString(&cloned.User, raw, force)
		case "service_tier":
			return setString(&cloned.ServiceTier, raw, force)
		case "tool_choice":
			return setAny(&cloned.ToolChoice, raw, force)
		default:
			// A forced null still strips the key the client sent.
			if _, skip := responsesOnlyKeys[key]; skip && raw != nil {
				return nil
			}
			return a.route(key, raw, force)
		}
	}
	if err := a.run(defaults, params, apply); err != nil {
		return nil, err
	}
	if a.touchesTyped() {
		// The frozen body never changes, so keep it out of the round trip.
		messages, tools, plan := cloned.Messages, cloned.Tools, cloned.PromptCachePlan
		cloned.Messages, cloned.Tools = nil, nil
		if err := mergeTyped(a, &cloned); err != nil {
			return nil, err
		}
		cloned.Messages, cloned.Tools, cloned.PromptCachePlan = messages, tools, plan
	}
	fields, err := a.extraFields(req.ExtraFields)
	if err != nil {
		return nil, err
	}
	cloned.ExtraFields = fields
	return &cloned, nil
}

// ApplyResponses is ApplyChat for the Responses API. Keys keep Chat
// Completions spelling: any token-limit spelling sets max_output_tokens.
// Chat members the Responses API rejects are skipped, response_format and
// verbosity included: a format for this API is set through text.
func ApplyResponses(req *core.ResponsesRequest, defaults, params Set) (*core.ResponsesRequest, error) {
	if req == nil || (len(defaults) == 0 && len(params) == 0) {
		return req, nil
	}
	cloned := *req
	a := newApplier(responsesTypedKeys, req.ExtraFields)
	apply := func(key string, raw json.RawMessage, force bool) error {
		switch key {
		case "temperature":
			return setPtr(&cloned.Temperature, raw, force)
		case "top_p":
			return setPtr(&cloned.TopP, raw, force)
		case "max_tokens", "max_completion_tokens", "max_output_tokens":
			if a.keepTokenLimit(cloned.MaxOutputTokens, force, responsesTokenLimitExtras...) {
				return nil
			}
			a.stripExtras(force, responsesTokenLimitExtras...)
			return setPtr(&cloned.MaxOutputTokens, raw, force)
		case "top_logprobs":
			return setPtr(&cloned.TopLogprobs, raw, force)
		case "parallel_tool_calls":
			return setPtr(&cloned.ParallelToolCalls, raw, force)
		case "store":
			return setPtr(&cloned.Store, raw, force)
		case "user":
			return setString(&cloned.User, raw, force)
		case "service_tier":
			return setString(&cloned.ServiceTier, raw, force)
		case "truncation":
			return setString(&cloned.Truncation, raw, force)
		case "safety_identifier":
			return setString(&cloned.SafetyIdentifier, raw, force)
		case "prompt_cache_retention":
			return setString(&cloned.PromptCacheRetention, raw, force)
		case "tool_choice":
			return setAny(&cloned.ToolChoice, raw, force)
		default:
			// A forced null still strips the key the client sent.
			if _, skip := chatOnlyKeys[key]; skip && raw != nil {
				return nil
			}
			return a.route(key, raw, force)
		}
	}
	if err := a.run(defaults, params, apply); err != nil {
		return nil, err
	}
	if a.touchesTyped() {
		// The frozen body never changes, so keep it out of the round trip.
		input, instructions, tools, prompt := cloned.Input, cloned.Instructions, cloned.Tools, cloned.Prompt
		cloned.Input, cloned.Instructions, cloned.Tools, cloned.Prompt = nil, "", nil, nil
		if err := mergeTyped(a, &cloned); err != nil {
			return nil, err
		}
		cloned.Input, cloned.Instructions, cloned.Tools, cloned.Prompt = input, instructions, tools, prompt
	}
	fields, err := a.extraFields(req.ExtraFields)
	if err != nil {
		return nil, err
	}
	cloned.ExtraFields = fields
	return &cloned, nil
}

var (
	chatTypedKeys      = typedJSONKeys(core.ChatRequest{})
	responsesTypedKeys = typedJSONKeys(core.ResponsesRequest{})
)

// One set serves every surface, so a key the target API does not have is
// skipped there rather than forwarded for the upstream to reject.
var (
	// chatOnlyKeys are Chat Completions members the Responses API rejects.
	// response_format and verbosity are skipped too: the Responses API sets
	// them in text, which an operator configures directly.
	chatOnlyKeys = map[string]struct{}{
		"seed": {}, "stop": {}, "n": {}, "frequency_penalty": {}, "presence_penalty": {},
		"logit_bias": {}, "logprobs": {}, "modalities": {}, "audio": {}, "prediction": {},
		"web_search_options": {}, "response_format": {}, "verbosity": {},
	}
	// responsesOnlyKeys are Responses API members Chat Completions rejects.
	responsesOnlyKeys = map[string]struct{}{
		"text": {}, "include": {}, "truncation": {}, "max_tool_calls": {}, "background": {},
		"context_management": {},
	}
)

// The token-limit spellings a surface keeps in ExtraFields rather than in
// its typed field.
var (
	chatTokenLimitExtras      = []string{"max_completion_tokens", "max_output_tokens"}
	responsesTokenLimitExtras = []string{"max_tokens", "max_completion_tokens"}
)

// keepTokenLimit reports whether a default token limit must leave the
// request alone: the token limit is one parameter to the client in any
// spelling, so a default applies only while none of them is set.
func (a *applier) keepTokenLimit(typed *int, force bool, extras ...string) bool {
	if force {
		return false
	}
	if typed != nil {
		return true
	}
	for _, key := range extras {
		if a.extraValue(key) != nil {
			return true
		}
	}
	return false
}

// stripExtras removes, for a forced value, the other spellings of the same
// parameter the client sent, so they cannot outbid it upstream.
func (a *applier) stripExtras(force bool, keys ...string) {
	if !force {
		return
	}
	for _, key := range keys {
		if a.extraValue(key) != nil {
			a.extras = ensureExtras(a.extras)
			a.extras[key] = nil
		}
	}
}

func ensureExtras(extras map[string]json.RawMessage) map[string]json.RawMessage {
	if extras == nil {
		return map[string]json.RawMessage{}
	}
	return extras
}

// extraValue returns an ExtraFields member as the applier currently sees it:
// a pending change if there is one, the client's value otherwise. A pending
// removal reads nil.
func (a *applier) extraValue(key string) json.RawMessage {
	if raw, pending := a.extras[key]; pending {
		return raw
	}
	return a.fields.Lookup(key)
}

// typedJSONKeys lists the JSON member names a request struct models, so the
// applier knows which keys the explicit switch did not cover but the struct
// still owns: those go through a JSON round trip rather than ExtraFields,
// where the marshaller would drop them.
func typedJSONKeys(v any) map[string]struct{} {
	t := reflect.TypeOf(v)
	keys := make(map[string]struct{}, t.NumField())
	for field := range t.Fields() {
		if !field.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		keys[name] = struct{}{}
	}
	return keys
}

// applier accumulates the keys the explicit switch did not handle: typed
// members merged through JSON and unknown members merged into ExtraFields.
type applier struct {
	typed  map[string]struct{}
	fields core.UnknownJSONFields

	merged  map[string]json.RawMessage
	removed []string
	// defaults marks merged keys recorded as defaults, which land only when
	// the encoded request lacks them.
	defaults map[string]struct{}
	// extras holds the pending ExtraFields members, nil marking a removal,
	// so a forced value layered over a default on the same key sees the
	// default rather than the client's original.
	extras map[string]json.RawMessage
}

func newApplier(typed map[string]struct{}, fields core.UnknownJSONFields) *applier {
	return &applier{typed: typed, fields: fields}
}

// run applies defaults, then params, through apply in key order.
func (a *applier) run(defaults, params Set, apply func(string, json.RawMessage, bool) error) error {
	for _, key := range sortedKeys(defaults) {
		if err := apply(key, defaults[key], false); err != nil {
			return fmt.Errorf("requestparams: default %q: %w", key, err)
		}
	}
	for _, key := range sortedKeys(params) {
		if err := apply(key, params[key], true); err != nil {
			return fmt.Errorf("requestparams: param %q: %w", key, err)
		}
	}
	return nil
}

func (a *applier) route(key string, raw json.RawMessage, force bool) error {
	if _, typed := a.typed[key]; typed {
		return a.routeTyped(key, raw, force)
	}
	return a.routeExtra(key, raw, force)
}

func (a *applier) routeTyped(key string, raw json.RawMessage, force bool) error {
	if !force {
		if a.merged == nil {
			a.merged = map[string]json.RawMessage{}
		}
		// Presence is decided against the encoded request in mergeTyped,
		// which is the only place the typed member is visible; a default
		// is recorded as such and dropped there when the key is set.
		a.merged[key] = raw
		if a.defaults == nil {
			a.defaults = map[string]struct{}{}
		}
		a.defaults[key] = struct{}{}
		return nil
	}
	delete(a.defaults, key)
	if raw == nil {
		a.removed = append(a.removed, key)
		delete(a.merged, key)
		return nil
	}
	if a.merged == nil {
		a.merged = map[string]json.RawMessage{}
	}
	a.merged[key] = raw
	return nil
}

func (a *applier) routeExtra(key string, raw json.RawMessage, force bool) error {
	current, pending := a.extras[key]
	if !pending {
		current = a.fields.Lookup(key)
	}
	if a.extras == nil {
		a.extras = map[string]json.RawMessage{}
	}
	switch {
	case raw == nil:
		// Only a forced set carries nil: the key is stripped.
		a.extras[key] = nil
	case current == nil:
		a.extras[key] = raw
	case isObject(current) && isObject(raw):
		merged, err := mergeObjects(current, raw, force)
		if err != nil {
			return err
		}
		a.extras[key] = merged
	case force:
		a.extras[key] = raw
	}
	return nil
}

func (a *applier) touchesTyped() bool {
	return len(a.merged) > 0 || len(a.removed) > 0
}

// mergeTyped applies typed members the switch does not model through a JSON
// round trip of the request: encode, set or remove the keys, decode again.
// A default only lands when the encoded request lacks the key, and objects
// merge one level deep like unknown members do. The result is decoded into
// a fresh value so no map or slice stays shared with the caller's request.
func mergeTyped[T any](a *applier, target *T) error {
	encoded, err := json.Marshal(target)
	if err != nil {
		return err
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &body); err != nil {
		return err
	}
	for _, key := range a.removed {
		delete(body, key)
	}
	for key, raw := range a.merged {
		current, set := body[key]
		_, isDefault := a.defaults[key]
		switch {
		case set && isObject(current) && isObject(raw):
			merged, err := mergeObjects(current, raw, !isDefault)
			if err != nil {
				return err
			}
			body[key] = merged
		case set && isDefault:
			continue
		default:
			body[key] = raw
		}
	}
	merged, err := json.Marshal(body)
	if err != nil {
		return err
	}
	var fresh T
	if err := json.Unmarshal(merged, &fresh); err != nil {
		return err
	}
	*target = fresh
	return nil
}

func (a *applier) extraFields(base core.UnknownJSONFields) (core.UnknownJSONFields, error) {
	if len(a.extras) == 0 {
		return base, nil
	}
	var removals []string
	additions := make(map[string]json.RawMessage, len(a.extras))
	for key, raw := range a.extras {
		if raw == nil {
			removals = append(removals, key)
			continue
		}
		additions[key] = raw
	}
	fields := base
	if len(removals) > 0 {
		fields = fields.Without(removals...)
	}
	return core.MergeUnknownJSONFields(fields, additions)
}

func isObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

// mergeObjects merges two JSON objects one level deep. When force is set the
// configured members win; otherwise the client's members are kept and only
// missing ones are filled in.
func mergeObjects(current, configured json.RawMessage, force bool) (json.RawMessage, error) {
	var base, over map[string]json.RawMessage
	if err := json.Unmarshal(current, &base); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(configured, &over); err != nil {
		return nil, err
	}
	for key, value := range over {
		if _, set := base[key]; set && !force {
			continue
		}
		base[key] = value
	}
	return json.Marshal(base)
}

func setPtr[T any](dst **T, raw json.RawMessage, force bool) error {
	if !force && *dst != nil {
		return nil
	}
	if raw == nil {
		*dst = nil
		return nil
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	*dst = &value
	return nil
}

func setString(dst *string, raw json.RawMessage, force bool) error {
	if !force && *dst != "" {
		return nil
	}
	if raw == nil {
		*dst = ""
		return nil
	}
	return json.Unmarshal(raw, dst)
}

func setAny(dst *any, raw json.RawMessage, force bool) error {
	if !force && *dst != nil {
		return nil
	}
	if raw == nil {
		*dst = nil
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	*dst = value
	return nil
}

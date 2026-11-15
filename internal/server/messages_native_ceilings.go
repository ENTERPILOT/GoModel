package server

import (
	"bytes"
	// encoding/json rather than goccy (ADR-0015): see messages_native.go.
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/anthropicapi"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers/anthropic"
	"github.com/enterpilot/gomodel/internal/requestparams"
)

// modelAlwaysThinks reports whether the model workflow resolved to cannot stop
// thinking, by the anthropic provider's own list. A ceiling that allows no
// thinking then asks a native body for the least the model accepts, as the
// provider does on the translated pipeline.
func modelAlwaysThinks(workflow *core.Workflow) bool {
	return workflow != nil && workflow.Resolution != nil && anthropic.AlwaysThinks(workflow.Resolution.ResolvedSelector.Model)
}

// messagesExceedCeilings reports whether a Messages request forwarded as it
// was sent would be above the ceilings.
func messagesExceedCeilings(req *anthropicapi.MessagesRequest, ceilings requestparams.Ceilings, alwaysThinks bool) bool {
	_, exceeds := anthropicapi.WithinCeilings(req, ceilings.ReasoningEffort, ceilings.OutputTokens, alwaysThinks)
	return exceeds
}

// messagesBodyWithinCeilings returns a native Messages body its caller's
// ceilings hold for, whichever decoder reads it. The body first gets a single
// member for each limit (see withSingleLimits); then max_tokens, the thinking
// config and output_config.effort are lowered where they stand. A body under
// no ceiling, or one within them that spells each limit once, is returned as
// it is. Lowering a value keeps every other byte; folding a repeated or
// re-cased member keeps every other member's bytes but rewrites the object,
// without the whitespace between its members. req is the body as decoded and
// alwaysThinks whether its model cannot stop thinking. A body the edits
// cannot bring within the ceilings is refused, never forwarded above them.
func messagesBodyWithinCeilings(body []byte, req *anthropicapi.MessagesRequest, ceilings requestparams.Ceilings, alwaysThinks bool) ([]byte, error) {
	if ceilings.IsZero() {
		return body, nil
	}
	body, err := lowerMessagesBody(body, req, ceilings, alwaysThinks)
	if err != nil {
		return nil, core.NewInvalidRequestError("the request exceeds the limits of its user path and could not be lowered to them", err)
	}
	return body, nil
}

// lowerMessagesBody folds and lowers body, then reads the result back the
// way the gateway decodes it: an edit that did not take fails here.
func lowerMessagesBody(body []byte, req *anthropicapi.MessagesRequest, ceilings requestparams.Ceilings, alwaysThinks bool) ([]byte, error) {
	body, folded := withSingleLimits(body)
	if folded {
		var err error
		if req, err = anthropicapi.DecodeMessagesRequest(body); err != nil {
			return nil, err
		}
	}
	lowered, exceeds := anthropicapi.WithinCeilings(req, ceilings.ReasoningEffort, ceilings.OutputTokens, alwaysThinks)
	if !exceeds {
		return body, nil
	}
	body, err := writeMessagesLimits(body, req, lowered)
	if err != nil {
		return nil, err
	}
	sent, err := anthropicapi.DecodeMessagesRequest(body)
	if err != nil {
		return nil, err
	}
	if _, folded := withSingleLimits(body); folded || messagesExceedCeilings(sent, ceilings, alwaysThinks) {
		return nil, errors.New("the lowered body is still above the ceilings")
	}
	return body, nil
}

// writeMessagesLimits writes the members in which lowered differs from req
// into body, which spells each of them once at most.
func writeMessagesLimits(body []byte, req, lowered *anthropicapi.MessagesRequest) ([]byte, error) {
	var err error
	if lowered.MaxTokens != req.MaxTokens {
		if body, err = replaceTopLevelMember(body, "max_tokens", []byte(strconv.Itoa(lowered.MaxTokens))); err != nil {
			return nil, err
		}
	}
	if thinking := lowered.Thinking; thinking != req.Thinking {
		// Lowering leaves thinking on with a smaller budget, or replaces it:
		// by "disabled", or by "adaptive" on a model that cannot stop thinking.
		if thinking.Type == "enabled" {
			body, err = setObjectMember(body, "thinking", "budget_tokens", []byte(strconv.Itoa(thinking.BudgetTokens)))
		} else {
			body, err = replaceTopLevelMember(body, "thinking", []byte(`{"type":"`+thinking.Type+`"}`))
		}
		if err != nil {
			return nil, err
		}
	}
	if lowered.OutputConfig != req.OutputConfig {
		effort, err := json.Marshal(lowered.OutputConfig.Effort)
		if err != nil {
			return nil, err
		}
		if body, err = setObjectMember(body, "output_config", "effort", effort); err != nil {
			return nil, err
		}
	}
	return body, nil
}

// setObjectMember returns body with member of its top-level key object set
// to value, keeping the object's other members. The object is added when
// body has none.
func setObjectMember(body []byte, key, member string, value []byte) ([]byte, error) {
	return editTopLevelMember(body, key, true, func(object json.RawMessage) ([]byte, error) {
		if trimmed := bytes.TrimSpace(object); len(trimmed) == 0 || trimmed[0] != '{' {
			object = json.RawMessage(`{}`)
		}
		return spliceTopLevelMember(object, member, value, true)
	})
}

// withSingleLimits returns a Messages body in which each member a ceiling
// bounds stands once, under the name Anthropic reads, and whether that
// changed it. A body may repeat a member or spell it in another letter case
// (MAX_TOKENS): the gateway's decoder reads the last of them, in any case,
// while an upstream may read the first, or only the exact name, so a limit
// checked in one could be forwarded in the other. One member is left for
// both to read: the lowest max_tokens, so a limit the client sent is never
// raised, and the last thinking and output_config, with the last type,
// budget_tokens and effort inside them.
func withSingleLimits(body []byte) ([]byte, bool) {
	body, folded := withSingleMember(body, "max_tokens", lowestTokenLimit)
	for _, control := range []struct {
		key     string
		members []string
	}{
		{"thinking", []string{"type", "budget_tokens"}},
		{"output_config", []string{"effort"}},
	} {
		var changed bool
		body, changed = withSingleMember(body, control.key, func(values []string) string {
			object := []byte(lastValue(values))
			for _, member := range control.members {
				object, _ = withSingleMember(object, member, lastValue)
			}
			return string(object)
		})
		folded = folded || changed
	}
	return body, folded
}

// withSingleMember returns object with the members that spell key, in any
// letter case and every occurrence, folded into one member named key where
// the last of them stood. pick chooses its value from theirs, in document
// order. object is returned as it is, with false, when key already stands
// once at most, under its own name and with the value pick chooses.
func withSingleMember(object []byte, key string, pick func(values []string) string) ([]byte, bool) {
	root := gjson.ParseBytes(object)
	if !root.IsObject() {
		return object, false
	}
	var values []string
	named := true
	root.ForEach(func(name, value gjson.Result) bool {
		if strings.EqualFold(name.String(), key) {
			values = append(values, value.Raw)
			named = named && name.String() == key
		}
		return true
	})
	if len(values) == 0 {
		return object, false
	}
	picked := pick(values)
	if len(values) == 1 && named && picked == values[0] {
		return object, false
	}
	buf := bytes.NewBuffer(make([]byte, 0, len(object)))
	buf.WriteByte('{')
	left := len(values)
	root.ForEach(func(name, value gjson.Result) bool {
		member, raw := name.Raw, value.Raw
		if strings.EqualFold(name.String(), key) {
			if left--; left > 0 {
				return true
			}
			member, raw = strconv.Quote(key), picked
		}
		if buf.Len() > 1 {
			buf.WriteByte(',')
		}
		buf.WriteString(member)
		buf.WriteByte(':')
		buf.WriteString(raw)
		return true
	})
	buf.WriteByte('}')
	return buf.Bytes(), true
}

// lowestTokenLimit returns the lowest of values that is a whole number of at
// least 1, or the last of them when none is: Anthropic takes nothing else
// for a limit.
func lowestTokenLimit(values []string) string {
	lowest, limit := lastValue(values), 0
	for _, value := range values {
		if n, err := strconv.Atoi(value); err == nil && n >= 1 && (limit == 0 || n < limit) {
			lowest, limit = value, n
		}
	}
	return lowest
}

func lastValue(values []string) string {
	return values[len(values)-1]
}

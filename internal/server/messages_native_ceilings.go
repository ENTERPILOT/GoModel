package server

import (
	"bytes"
	// encoding/json rather than goccy (ADR-0015): see messages_native.go.
	"encoding/json"
	"strconv"

	"github.com/enterpilot/gomodel/internal/anthropicapi"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/requestparams"
)

// messagesBodyWithinCeilings returns a native Messages body lowered to its
// caller's ceilings. A request the translated pipeline can carry reaches the
// native path only within them, so this changes the one that has no other
// path (server-tool history, ...): max_tokens, the thinking config and
// output_config.effort are edited where they stand and every other byte is
// kept. req is the body as decoded. A body the edits cannot bring within the
// ceilings is refused, never forwarded above them.
func messagesBodyWithinCeilings(body []byte, req *anthropicapi.MessagesRequest, ceilings requestparams.Ceilings) ([]byte, error) {
	lowered, changed := anthropicapi.WithinCeilings(req, ceilings.ReasoningEffort, ceilings.OutputTokens)
	if !changed {
		return body, nil
	}
	body, err := lowerMessagesBody(body, req, lowered)
	if err == nil {
		var sent *anthropicapi.MessagesRequest
		if sent, err = anthropicapi.DecodeMessagesRequest(body); err == nil {
			if _, exceeds := anthropicapi.WithinCeilings(sent, ceilings.ReasoningEffort, ceilings.OutputTokens); !exceeds {
				return body, nil
			}
		}
	}
	return nil, core.NewInvalidRequestError("the request exceeds the limits of its user path and could not be lowered to them", err)
}

// lowerMessagesBody writes the members in which lowered differs from req
// into body.
func lowerMessagesBody(body []byte, req, lowered *anthropicapi.MessagesRequest) ([]byte, error) {
	var err error
	if lowered.MaxTokens != req.MaxTokens {
		if body, err = replaceTopLevelMember(body, "max_tokens", []byte(strconv.Itoa(lowered.MaxTokens))); err != nil {
			return nil, err
		}
	}
	if thinking := lowered.Thinking; thinking != req.Thinking {
		// Lowering leaves thinking on with a smaller budget, or turns it off.
		if thinking.Type == "enabled" {
			body, err = setObjectMember(body, "thinking", "budget_tokens", []byte(strconv.Itoa(thinking.BudgetTokens)))
		} else {
			body, err = replaceTopLevelMember(body, "thinking", []byte(`{"type":"disabled"}`))
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

package pluginapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Tool is a tool definition sent with the request. Read-only in this version.
type Tool struct {
	Name        string
	Description string
	// Parameters is the JSON schema of the tool's arguments.
	Parameters json.RawMessage
	// Raw is the tool definition as sent.
	Raw json.RawMessage
}

// Params are the request parameters GoModel models. Edit them through
// [Prompt.SetParam]; direct assignment is not applied.
type Params struct {
	// Model is the model the request is addressed to after routing.
	Model string
	// MaxTokens is the completion length cap (chat max_tokens, Responses
	// max_output_tokens); nil when unset.
	MaxTokens   *int
	Temperature *float64
	TopP        *float64
	// Stream reports a streaming request.
	Stream bool
	// ToolChoice is the tool_choice value as sent (string or object).
	ToolChoice any
	// Extra is a read-only view of the other body-level fields, keyed by
	// their JSON name.
	Extra map[string]any
}

// Prompt is the unified request: the conversation, tools, and parameters.
// Read it freely; edit it only through its methods so the host can re-encode
// exactly what changed.
type Prompt struct {
	// Messages is the conversation in order, including system messages.
	Messages []Message
	// Tools are the tool definitions sent with the request.
	Tools []Tool
	// Params are the modelled request parameters.
	Params Params
	// Raw is the request body as received. Read-only.
	Raw json.RawMessage

	changes Changes
	removed []Message
	nextID  int
}

// ToolCallRef locates a tool call in the conversation.
type ToolCallRef struct {
	// MessageID is the message holding the call.
	MessageID string
	Call      ToolCall
	// HasResult reports whether a tool result for the call is present.
	HasResult bool
}

// DanglingToolError is returned by [Prompt.Remove] when the removal leaves a
// tool call without its result or a result without its call. The removal is
// applied; remove PartnerID as well to make the conversation consistent
// again. The host rejects a prompt that still has dangling pairs.
type DanglingToolError struct {
	// MessageID is the removed message.
	MessageID string
	// PartnerID is the message that now lacks its call or result.
	PartnerID string
	// CallID is the tool call whose pairing broke.
	CallID string
}

func (e *DanglingToolError) Error() string {
	return fmt.Sprintf("pluginapi: removing message %q leaves tool call %q dangling in message %q; remove it too", e.MessageID, e.CallID, e.PartnerID)
}

// LastUser returns the most recent user message, or nil.
func (p *Prompt) LastUser() *Message {
	for i := len(p.Messages) - 1; i >= 0; i-- {
		if p.Messages[i].Role == RoleUser {
			return &p.Messages[i]
		}
	}
	return nil
}

// Text returns the text parts of every message with one of the given roles
// (all roles when none is given), joined by newlines.
func (p *Prompt) Text(roles ...Role) string {
	var texts []string
	for _, m := range p.Messages {
		if len(roles) > 0 && !containsRole(roles, m.Role) {
			continue
		}
		for _, part := range m.Parts {
			if part.Kind == PartText {
				texts = append(texts, part.Text)
			}
		}
	}
	return strings.Join(texts, "\n")
}

// SystemText returns the text of system and developer messages.
func (p *Prompt) SystemText() string {
	return p.Text(RoleSystem, RoleDeveloper)
}

// ToolCalls lists every tool call in the conversation with the message that
// holds it and whether a matching result exists.
func (p *Prompt) ToolCalls() []ToolCallRef {
	results := map[string]bool{}
	for _, m := range p.Messages {
		for _, part := range m.Parts {
			if part.Kind == PartToolResult && part.ToolResult != nil {
				results[part.ToolResult.CallID] = true
			}
		}
	}
	var refs []ToolCallRef
	for _, m := range p.Messages {
		for _, part := range m.Parts {
			if part.Kind == PartToolCall && part.ToolCall != nil {
				refs = append(refs, ToolCallRef{MessageID: m.ID, Call: *part.ToolCall, HasResult: results[part.ToolCall.ID]})
			}
		}
	}
	return refs
}

// NewSince returns the messages after the first n, for plugins that only
// scan new turns.
func (p *Prompt) NewSince(n int) []Message {
	if n < 0 {
		n = 0
	}
	if n >= len(p.Messages) {
		return nil
	}
	return p.Messages[n:]
}

// Message returns the message with the given ID, or nil. The pointer is
// valid until the next Insert, Append, or Remove.
func (p *Prompt) Message(id string) *Message {
	for i := range p.Messages {
		if p.Messages[i].ID == id {
			return &p.Messages[i]
		}
	}
	return nil
}

// SetText replaces the text of part partIdx of message msgID. The part must
// be a text part.
func (p *Prompt) SetText(msgID string, partIdx int, text string) error {
	m := p.Message(msgID)
	if m == nil {
		return unknownMessage(msgID)
	}
	if partIdx < 0 || partIdx >= len(m.Parts) {
		return fmt.Errorf("pluginapi: message %q has no part %d", msgID, partIdx)
	}
	if m.Parts[partIdx].Kind != PartText {
		return fmt.Errorf("pluginapi: part %d of message %q is %s, not text", partIdx, msgID, m.Parts[partIdx].Kind)
	}
	m.Parts[partIdx].Text = text
	p.changes.mark(msgID, ChangeEdited)
	return nil
}

// SetToolArguments replaces the arguments of tool call callID in message
// msgID. args must be valid JSON.
func (p *Prompt) SetToolArguments(msgID, callID string, args json.RawMessage) error {
	m := p.Message(msgID)
	if m == nil {
		return unknownMessage(msgID)
	}
	if !json.Valid(args) {
		return fmt.Errorf("pluginapi: tool arguments for call %q are not valid JSON", callID)
	}
	for i := range m.Parts {
		part := &m.Parts[i]
		if part.Kind == PartToolCall && part.ToolCall != nil && part.ToolCall.ID == callID {
			call := *part.ToolCall
			call.Arguments = append(json.RawMessage(nil), args...)
			part.ToolCall = &call
			p.changes.mark(msgID, ChangeEdited)
			return nil
		}
	}
	return fmt.Errorf("pluginapi: message %q has no tool call %q", msgID, callID)
}

// SetToolResult replaces the content of the result for call callID in
// message msgID.
func (p *Prompt) SetToolResult(msgID, callID string, parts []Part) error {
	m := p.Message(msgID)
	if m == nil {
		return unknownMessage(msgID)
	}
	for i := range m.Parts {
		part := &m.Parts[i]
		if part.Kind == PartToolResult && part.ToolResult != nil && part.ToolResult.CallID == callID {
			result := *part.ToolResult
			result.Parts = append([]Part(nil), parts...)
			part.ToolResult = &result
			p.changes.mark(msgID, ChangeEdited)
			return nil
		}
	}
	return fmt.Errorf("pluginapi: message %q has no tool result for call %q", msgID, callID)
}

// Insert adds m at position at (clamped to the conversation bounds) and
// returns its generated ID. Any ID on m is replaced.
func (p *Prompt) Insert(at int, m Message) string {
	if at < 0 {
		at = 0
	}
	if at > len(p.Messages) {
		at = len(p.Messages)
	}
	m.ID = p.newID()
	p.Messages = append(p.Messages, Message{})
	copy(p.Messages[at+1:], p.Messages[at:])
	p.Messages[at] = m
	p.changes.mark(m.ID, ChangeInserted)
	return m.ID
}

// Append adds m at the end of the conversation and returns its generated ID.
func (p *Prompt) Append(m Message) string {
	return p.Insert(len(p.Messages), m)
}

// Remove drops the message with the given ID. Removing a message a plugin
// inserted just forgets it; removing an original message is recorded for the
// host. When the removal leaves a tool call without its result (or the
// reverse) the message is still removed and a [DanglingToolError] names the
// partner message to remove next.
func (p *Prompt) Remove(msgID string) error {
	idx := -1
	for i := range p.Messages {
		if p.Messages[i].ID == msgID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return unknownMessage(msgID)
	}
	removed := p.Messages[idx]
	p.Messages = append(p.Messages[:idx], p.Messages[idx+1:]...)
	if p.changes.Messages[msgID] == ChangeInserted {
		delete(p.changes.Messages, msgID)
	} else {
		p.changes.mark(msgID, ChangeRemoved)
		p.removed = append(p.removed, removed)
	}
	return p.dangling(removed)
}

// SetParam records a request parameter change: "max_tokens", "temperature",
// "top_p", or any other body-level key. The typed Params fields are updated
// for the three modelled names; the host decides which other keys it can
// apply. "model" and "stream" cannot be changed after routing.
func (p *Prompt) SetParam(name string, value any) {
	switch name {
	case "max_tokens":
		if n, ok := toInt(value); ok {
			p.Params.MaxTokens = &n
		}
	case "temperature":
		if f, ok := toFloat(value); ok {
			p.Params.Temperature = &f
		}
	case "top_p":
		if f, ok := toFloat(value); ok {
			p.Params.TopP = &f
		}
	}
	p.changes.setParam(name, value)
}

// Changes reports what was edited. Host-facing; plugins do not need it.
func (p *Prompt) Changes() Changes {
	return p.changes.clone()
}

// Reset clears change tracking after the host has built the prompt or
// applied the edits. Host-facing; plugin authors never call it.
func (p *Prompt) Reset() {
	p.changes = Changes{}
	p.removed = nil
}

// Validate reports the first tool call/result pair a removal broke, as a
// [DanglingToolError]. Host-facing: the host calls it before applying edits.
func (p *Prompt) Validate() error {
	for _, m := range p.removed {
		if err := p.dangling(m); err != nil {
			return err
		}
	}
	return nil
}

func (p *Prompt) dangling(removed Message) error {
	for _, part := range removed.Parts {
		switch {
		case part.Kind == PartToolCall && part.ToolCall != nil:
			if partner := p.findToolPart(PartToolResult, part.ToolCall.ID); partner != "" {
				return &DanglingToolError{MessageID: removed.ID, PartnerID: partner, CallID: part.ToolCall.ID}
			}
		case part.Kind == PartToolResult && part.ToolResult != nil:
			if partner := p.findToolPart(PartToolCall, part.ToolResult.CallID); partner != "" {
				return &DanglingToolError{MessageID: removed.ID, PartnerID: partner, CallID: part.ToolResult.CallID}
			}
		}
	}
	return nil
}

func (p *Prompt) findToolPart(kind PartKind, callID string) string {
	for _, m := range p.Messages {
		for _, part := range m.Parts {
			if part.Kind != kind {
				continue
			}
			if kind == PartToolCall && part.ToolCall != nil && part.ToolCall.ID == callID {
				return m.ID
			}
			if kind == PartToolResult && part.ToolResult != nil && part.ToolResult.CallID == callID {
				return m.ID
			}
		}
	}
	return ""
}

func (p *Prompt) newID() string {
	for {
		p.nextID++
		id := fmt.Sprintf("new-%d", p.nextID)
		if p.Message(id) == nil && p.changes.Messages[id] == "" {
			return id
		}
	}
}

func unknownMessage(id string) error {
	return fmt.Errorf("pluginapi: %w: %q", errUnknownMessage, id)
}

var errUnknownMessage = errors.New("unknown message")

func containsRole(roles []Role, role Role) bool {
	return slices.Contains(roles, role)
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case float32:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	case *int:
		if n == nil {
			return 0, false
		}
		return *n, true
	}
	return 0, false
}

func toFloat(v any) (float64, bool) {
	switch f := v.(type) {
	case float64:
		return f, true
	case float32:
		return float64(f), true
	case int:
		return float64(f), true
	case int64:
		return float64(f), true
	case json.Number:
		x, err := f.Float64()
		return x, err == nil
	case *float64:
		if f == nil {
			return 0, false
		}
		return *f, true
	}
	return 0, false
}

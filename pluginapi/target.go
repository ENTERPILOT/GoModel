package pluginapi

import "fmt"

// TextTarget locates one piece of editable text: a text part of a prompt
// message, a text part inside a tool result, or a text part of a completion
// choice. Plugins that scan or rewrite text list targets with
// [Prompt.TextTargets] or [Completion.TextTargets] and write back with the
// matching SetTargetText, instead of walking parts by hand.
type TextTarget struct {
	// MessageID is the prompt message holding the text. Empty for a
	// completion target.
	MessageID string
	// Choice is the completion choice holding the text. Zero for a prompt
	// target.
	Choice int
	// Role is the role of the message holding the text; [RoleAssistant] for
	// completion targets.
	Role Role
	// Part is the index in Message.Parts of the text part, or of the tool
	// result that holds it when CallID is set.
	Part int
	// CallID is the tool call whose result holds the text. Empty for a plain
	// text part.
	CallID string
	// ResultPart is the index in ToolResult.Parts of the text part when
	// CallID is set.
	ResultPart int
	// Text is the text as it was when the target was listed.
	Text string
}

// TextTargets lists every text part of the messages with one of the given
// roles (all roles when none is given), in conversation order. Text inside
// tool results is included as its own target; text split across parts is
// reported as separate targets.
func (p *Prompt) TextTargets(roles ...Role) []TextTarget {
	var out []TextTarget
	for _, m := range p.Messages {
		if len(roles) > 0 && !containsRole(roles, m.Role) {
			continue
		}
		for i, part := range m.Parts {
			switch part.Kind {
			case PartText:
				out = append(out, TextTarget{MessageID: m.ID, Role: m.Role, Part: i, Text: part.Text})
			case PartToolResult:
				if part.ToolResult == nil {
					continue
				}
				for j, inner := range part.ToolResult.Parts {
					if inner.Kind != PartText {
						continue
					}
					out = append(out, TextTarget{MessageID: m.ID, Role: m.Role, Part: i, CallID: part.ToolResult.CallID, ResultPart: j, Text: inner.Text})
				}
			}
		}
	}
	return out
}

// SetTargetText replaces the text of a target listed by [Prompt.TextTargets].
// It reads the current state of the message, so successive edits to
// different text parts of one tool result compose.
func (p *Prompt) SetTargetText(t TextTarget, text string) error {
	if t.CallID == "" {
		return p.SetText(t.MessageID, t.Part, text)
	}
	m := p.Message(t.MessageID)
	if m == nil {
		return unknownMessage(t.MessageID)
	}
	if t.Part < 0 || t.Part >= len(m.Parts) {
		return fmt.Errorf("pluginapi: message %q has no part %d", t.MessageID, t.Part)
	}
	holder := m.Parts[t.Part]
	if holder.Kind != PartToolResult || holder.ToolResult == nil || holder.ToolResult.CallID != t.CallID {
		return fmt.Errorf("pluginapi: part %d of message %q is not the result of tool call %q", t.Part, t.MessageID, t.CallID)
	}
	parts := append([]Part(nil), holder.ToolResult.Parts...)
	if t.ResultPart < 0 || t.ResultPart >= len(parts) {
		return fmt.Errorf("pluginapi: tool result %q of message %q has no part %d", t.CallID, t.MessageID, t.ResultPart)
	}
	if parts[t.ResultPart].Kind != PartText {
		return fmt.Errorf("pluginapi: part %d of tool result %q in message %q is %s, not text", t.ResultPart, t.CallID, t.MessageID, parts[t.ResultPart].Kind)
	}
	parts[t.ResultPart].Text = text
	return p.SetToolResult(t.MessageID, t.CallID, parts)
}

// TextTargets lists every text part of every choice, in order.
func (c *Completion) TextTargets() []TextTarget {
	var out []TextTarget
	for i, choice := range c.Choices {
		for j, part := range choice.Message.Parts {
			if part.Kind != PartText {
				continue
			}
			out = append(out, TextTarget{Choice: i, Role: RoleAssistant, Part: j, Text: part.Text})
		}
	}
	return out
}

// SetTargetText replaces the text of a target listed by
// [Completion.TextTargets].
func (c *Completion) SetTargetText(t TextTarget, text string) error {
	return c.SetText(t.Choice, t.Part, text)
}

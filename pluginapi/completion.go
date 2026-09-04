package pluginapi

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Usage is token accounting in provider-neutral names.
type Usage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	// CachedInputTokens is the part of InputTokens served from a prompt cache.
	CachedInputTokens int
}

// Choice is one completion candidate. Chat completions may return several;
// Responses and Anthropic return one.
type Choice struct {
	Index int
	// Message uses the same Part model as the request: text, tool_call,
	// reasoning, refusal.
	Message Message
	// FinishReason is the OpenAI-style reason: "stop", "length",
	// "tool_calls", "content_filter".
	FinishReason string
}

// Completion is the unified response. Read it freely; edit it through its
// methods so the host re-encodes only what changed.
type Completion struct {
	ID      string
	Model   string
	Choices []Choice
	Usage   Usage
	// Raw is the response as received from the provider. Read-only.
	Raw json.RawMessage

	changes Changes
}

// Text returns the text parts of the given choice concatenated, or "" when
// the choice does not exist.
func (c *Completion) Text(choice int) string {
	if choice < 0 || choice >= len(c.Choices) {
		return ""
	}
	var out strings.Builder
	for _, part := range c.Choices[choice].Message.Parts {
		if part.Kind == PartText {
			out.WriteString(part.Text)
		}
	}
	return out.String()
}

// SetText replaces the text of part partIdx of the given choice. The part
// must be a text part.
func (c *Completion) SetText(choice, partIdx int, text string) error {
	ch, err := c.choice(choice)
	if err != nil {
		return err
	}
	if partIdx < 0 || partIdx >= len(ch.Message.Parts) {
		return fmt.Errorf("pluginapi: choice %d has no part %d", choice, partIdx)
	}
	if ch.Message.Parts[partIdx].Kind != PartText {
		return fmt.Errorf("pluginapi: part %d of choice %d is %s, not text", partIdx, choice, ch.Message.Parts[partIdx].Kind)
	}
	ch.Message.Parts[partIdx].Text = text
	c.changes.mark(choiceKey(choice), ChangeEdited)
	return nil
}

// SetFinishReason sets the finish reason of the given choice.
// "content_filter" is the OpenAI-compatible way to say the response was cut.
func (c *Completion) SetFinishReason(choice int, reason string) error {
	ch, err := c.choice(choice)
	if err != nil {
		return err
	}
	ch.FinishReason = reason
	c.changes.mark(choiceKey(choice), ChangeEdited)
	return nil
}

// ReplaceText drops every text part of the choice and keeps a single text
// part with the given text, placed where the first text part was (or first).
// Tool calls and other non-text parts are kept. Used for redaction and
// synthetic answers.
func (c *Completion) ReplaceText(choice int, text string) error {
	ch, err := c.choice(choice)
	if err != nil {
		return err
	}
	parts := make([]Part, 0, len(ch.Message.Parts)+1)
	inserted := false
	for _, part := range ch.Message.Parts {
		if part.Kind == PartText {
			if !inserted {
				parts = append(parts, Part{Kind: PartText, Text: text})
				inserted = true
			}
			continue
		}
		parts = append(parts, part)
	}
	if !inserted {
		parts = append([]Part{{Kind: PartText, Text: text}}, parts...)
	}
	ch.Message.Parts = parts
	c.changes.mark(choiceKey(choice), ChangeReplaced)
	return nil
}

// Changes reports what was edited, keyed by "choice:<index>" where index is
// the position in Choices. Host-facing.
func (c *Completion) Changes() Changes {
	return c.changes.clone()
}

// Reset clears change tracking. Host-facing; plugin authors never call it.
func (c *Completion) Reset() {
	c.changes = Changes{}
}

func (c *Completion) choice(i int) (*Choice, error) {
	if i < 0 || i >= len(c.Choices) {
		return nil, fmt.Errorf("pluginapi: completion has no choice %d", i)
	}
	return &c.Choices[i], nil
}

func choiceKey(i int) string {
	return fmt.Sprintf("choice:%d", i)
}

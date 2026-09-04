package pluginapi

import "maps"

// ChangeKind says how a message (or completion choice) was edited.
type ChangeKind string

const (
	// ChangeEdited marks a message whose parts or parameters were rewritten
	// in place; the host re-encodes only the touched parts.
	ChangeEdited ChangeKind = "edited"
	// ChangeInserted marks a message added by a plugin; the host encodes it
	// from the unified form.
	ChangeInserted ChangeKind = "inserted"
	// ChangeRemoved marks an original message a plugin removed.
	ChangeRemoved ChangeKind = "removed"
	// ChangeReplaced marks a completion choice whose text was replaced
	// wholesale with [Completion.ReplaceText].
	ChangeReplaced ChangeKind = "replaced"
)

// Changes is the host-facing edit record of a [Prompt] or [Completion].
type Changes struct {
	// Messages maps message IDs (or "choice:<index>" for completions) to the
	// kind of change. Untouched messages are absent.
	Messages map[string]ChangeKind
	// Params holds parameters set through SetParam, by name.
	Params map[string]any
	// Dirty is true after any edit.
	Dirty bool
}

func (c *Changes) mark(id string, kind ChangeKind) {
	if c.Messages == nil {
		c.Messages = map[string]ChangeKind{}
	}
	c.Dirty = true
	switch kind {
	case ChangeEdited:
		// An inserted or replaced message stays in its stronger state.
		if _, ok := c.Messages[id]; ok {
			return
		}
	case ChangeReplaced:
		if c.Messages[id] == ChangeInserted {
			return
		}
	}
	c.Messages[id] = kind
}

func (c *Changes) setParam(name string, value any) {
	if c.Params == nil {
		c.Params = map[string]any{}
	}
	c.Params[name] = value
	c.Dirty = true
}

// clone returns a copy the caller may keep after further edits.
func (c Changes) clone() Changes {
	out := Changes{Dirty: c.Dirty}
	if c.Messages != nil {
		out.Messages = make(map[string]ChangeKind, len(c.Messages))
		maps.Copy(out.Messages, c.Messages)
	}
	if c.Params != nil {
		out.Params = make(map[string]any, len(c.Params))
		maps.Copy(out.Params, c.Params)
	}
	return out
}

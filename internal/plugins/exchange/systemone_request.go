package exchange

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"
)

// SystemOneStateMessageID is the ID of the user message built from a System
// One request's state.
const SystemOneStateMessageID = "state"

// FromSystemOneRequest builds the unified prompt for a System One decision
// request. The state is the only content a caller supplies per request, so it
// becomes the prompt's single user message: a string state as its text, any
// other JSON value as its encoded JSON. The questions are the application's
// fixed schema and are not exposed.
func FromSystemOneRequest(req *core.SystemOneRequest) (*pluginapi.Prompt, error) {
	if req == nil {
		return nil, fmt.Errorf("exchange: nil System One request")
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("exchange: encode System One request: %w", err)
	}
	p := &pluginapi.Prompt{
		Raw:      raw,
		Messages: []pluginapi.Message{pluginapi.TextMessage(pluginapi.RoleUser, systemOneStateText(req.State))},
		Params:   pluginapi.Params{Model: req.Model},
	}
	p.Messages[0].ID = SystemOneStateMessageID
	p.Reset()
	return p, nil
}

func systemOneStateText(state json.RawMessage) string {
	var text string
	if trimmed := bytes.TrimSpace(state); len(trimmed) > 0 && trimmed[0] == '"' && json.Unmarshal(trimmed, &text) == nil {
		return text
	}
	return string(state)
}

// ApplyToSystemOneRequest returns a copy of original with the state
// message's edits applied. A string state stays a string; a state sent as
// another JSON value must still be valid JSON after the edit. Removing the
// state is an error, since the request would have nothing to decide on.
// Edits System One has no place for, such as inserted messages or parameter
// changes, are not applied; SystemOneUncarriedEdits lists them.
func ApplyToSystemOneRequest(original *core.SystemOneRequest, p *pluginapi.Prompt) (*core.SystemOneRequest, error) {
	if original == nil || p == nil {
		return nil, fmt.Errorf("exchange: nil System One request or prompt")
	}
	result := *original
	switch p.Changes().Messages[SystemOneStateMessageID] {
	case "":
		return &result, nil
	case pluginapi.ChangeRemoved:
		return nil, fmt.Errorf("exchange: the System One state was removed")
	}
	msg := p.Message(SystemOneStateMessageID)
	if msg == nil {
		return nil, fmt.Errorf("exchange: the System One state was removed")
	}
	text := msg.Text()
	// A missing state becomes a string; null, numbers, and records keep
	// their JSON type (json.Unmarshal would accept null into a string).
	state := bytes.TrimSpace(original.State)
	if len(state) == 0 || state[0] == '"' {
		encoded, err := json.Marshal(text)
		if err != nil {
			return nil, fmt.Errorf("exchange: encode System One state: %w", err)
		}
		result.State = encoded
		return &result, nil
	}
	if !json.Valid([]byte(text)) {
		return nil, fmt.Errorf("exchange: the edited System One state is no longer valid JSON")
	}
	result.State = json.RawMessage(text)
	return &result, nil
}

// SystemOneUncarriedEdits describes the prompt edits ApplyToSystemOneRequest
// does not apply, in a stable order, or nil when every edit was carried.
func SystemOneUncarriedEdits(p *pluginapi.Prompt) []string {
	if p == nil {
		return nil
	}
	changes := p.Changes()
	var uncarried []string
	for id, kind := range changes.Messages {
		if id != SystemOneStateMessageID {
			uncarried = append(uncarried, fmt.Sprintf("%s message %q", kind, id))
		}
	}
	for name := range changes.Params {
		uncarried = append(uncarried, fmt.Sprintf("parameter %q", name))
	}
	sort.Strings(uncarried)
	return uncarried
}

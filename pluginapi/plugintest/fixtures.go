package plugintest

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/enterpilot/gomodel/pluginapi"
)

// Init builds a plugin from factory and initializes it with cfg (JSON, or
// empty for no configuration) and host; a nil host selects a fresh [Host].
// It fails the test when Init returns an error.
func Init(t testing.TB, factory func() pluginapi.Plugin, cfg string, host pluginapi.Host) pluginapi.Plugin {
	t.Helper()
	if host == nil {
		host = NewHost()
	}
	p := factory()
	if err := p.Init(context.Background(), json.RawMessage(cfg), host); err != nil {
		t.Fatalf("plugintest: Init(%s): %v", cfg, err)
	}
	return p
}

// Text returns a single-text message with the given role and ID.
func Text(role pluginapi.Role, id, text string) pluginapi.Message {
	m := pluginapi.TextMessage(role, text)
	m.ID = id
	return m
}

// Prompt returns a prompt holding msgs, with clean change tracking.
func Prompt(msgs ...pluginapi.Message) *pluginapi.Prompt {
	p := &pluginapi.Prompt{Messages: msgs}
	p.Reset()
	return p
}

// Completion returns a completion with one assistant text choice per text,
// each finished with "stop".
func Completion(texts ...string) *pluginapi.Completion {
	c := &pluginapi.Completion{}
	for i, text := range texts {
		c.Choices = append(c.Choices, pluginapi.Choice{
			Index: i, Message: pluginapi.TextMessage(pluginapi.RoleAssistant, text), FinishReason: "stop",
		})
	}
	return c
}

// Exchange returns an exchange for a hook call, with a Values bag, a
// stream state, and a request ID in Meta. Either argument may be nil.
func Exchange(prompt *pluginapi.Prompt, resp *pluginapi.Completion) *pluginapi.Exchange {
	return &pluginapi.Exchange{
		Meta:     pluginapi.Meta{RequestID: "test-request"},
		Prompt:   prompt,
		Response: resp,
		Stream:   &pluginapi.StreamState{},
		Values:   pluginapi.Values{},
	}
}

// TextDelta returns a text delta event for choice 0.
func TextDelta(text string) *pluginapi.StreamEvent {
	return &pluginapi.StreamEvent{Kind: pluginapi.EventTextDelta, Text: text}
}

// Event returns an event of the given kind for choice 0.
func Event(kind pluginapi.EventKind) *pluginapi.StreamEvent {
	return &pluginapi.StreamEvent{Kind: kind}
}

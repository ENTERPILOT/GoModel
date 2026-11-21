package providers

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func messageExtras(t *testing.T, raw string) core.UnknownJSONFields {
	t.Helper()
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(raw), &fields))
	return core.UnknownJSONFieldsFromMap(fields)
}

// The assistant message an OpenAI SDK dump replays: every unset member is
// spelled out as null.
const replayedAssistantExtras = `{"refusal":null,"annotations":[],"audio":null,"function_call":null,"name":"bot","reasoning_content":"earlier"}`

func TestDropMessageMembers(t *testing.T) {
	req := &core.ChatRequest{Model: "m", Messages: []core.Message{
		{Role: "user", Content: "hi", ExtraFields: messageExtras(t, `{"name":"alice","annotations":[]}`)},
		{Role: "assistant", Content: "hello", ExtraFields: messageExtras(t, replayedAssistantExtras)},
		{Role: "assistant", Content: "plain"},
	}}

	got := DropMessageMembers(req, "assistant", "refusal", "annotations", "audio")

	require.NotSame(t, req, got)
	assistant := got.Messages[1].ExtraFields
	assert.False(t, assistant.HasAny("refusal", "annotations", "audio"))
	assert.Equal(t, json.RawMessage(`null`), assistant.Lookup("function_call"), "members that are not named stay")
	assert.Equal(t, json.RawMessage(`"bot"`), assistant.Lookup("name"))
	assert.Equal(t, json.RawMessage(`"earlier"`), assistant.Lookup("reasoning_content"))
	assert.Equal(t, json.RawMessage(`[]`), got.Messages[0].ExtraFields.Lookup("annotations"), "other roles are left alone")
	assert.True(t, req.Messages[1].ExtraFields.HasAny("refusal"), "the caller's request must not change")
}

func TestDropNullMessageMembers(t *testing.T) {
	req := &core.ChatRequest{Model: "m", Messages: []core.Message{
		{Role: "assistant", Content: "a", ExtraFields: messageExtras(t, replayedAssistantExtras)},
		{Role: "assistant", Content: "b", ExtraFields: messageExtras(t, `{"refusal":"no","function_call":{"name":"f","arguments":"{}"}}`)},
	}}

	got := DropNullMessageMembers(req, "assistant", "refusal", "audio", "function_call")

	require.NotSame(t, req, got)
	first := got.Messages[0].ExtraFields
	assert.False(t, first.HasAny("refusal", "audio", "function_call"))
	assert.Equal(t, json.RawMessage(`[]`), first.Lookup("annotations"))
	second := got.Messages[1].ExtraFields
	assert.Equal(t, json.RawMessage(`"no"`), second.Lookup("refusal"), "a member with a value stays")
	assert.JSONEq(t, `{"name":"f","arguments":"{}"}`, string(second.Lookup("function_call")))
}

// A client can send a key twice, and the gateway keeps both. Which one a
// null-intolerant upstream reads is its parser's choice, so neither may reach
// it when one of them is null.
func TestDropNullMessageMembers_DropsAKeySentTwiceWhenEitherIsNull(t *testing.T) {
	var req core.ChatRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"m","messages":[`+
		`{"role":"assistant","content":"a","refusal":"no","refusal":null},`+
		`{"role":"assistant","content":"b","refusal":null,"refusal":"no","name":"bot"}]}`), &req))

	got := DropNullMessageMembers(&req, "assistant", "refusal")

	for i, message := range got.Messages {
		assert.False(t, message.ExtraFields.HasAny("refusal"), "message %d", i)
	}
	assert.Equal(t, json.RawMessage(`"bot"`), got.Messages[1].ExtraFields.Lookup("name"))
}

func TestDropMessageMembers_ReturnsRequestWhenNothingChanges(t *testing.T) {
	req := &core.ChatRequest{Model: "m", Messages: []core.Message{
		{Role: "user", Content: "hi", ExtraFields: messageExtras(t, `{"refusal":null}`)},
		{Role: "assistant", Content: "hello", ExtraFields: messageExtras(t, `{"refusal":"no","name":"bot"}`)},
	}}

	assert.Same(t, req, DropMessageMembers(req, "assistant", "annotations", "audio"))
	assert.Same(t, req, DropMessageMembers(req, "assistant"))
	assert.Same(t, req, DropNullMessageMembers(req, "assistant", "refusal", "audio"))
	assert.Nil(t, DropMessageMembers(nil, "assistant", "refusal"))
}

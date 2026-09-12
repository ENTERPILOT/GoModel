package core

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCloneBatchRequestDeepCopiesNestedFields(t *testing.T) {
	original := &BatchRequest{
		InputFileID:      "file_source",
		Endpoint:         "/v1/chat/completions",
		CompletionWindow: "24h",
		Metadata: map[string]string{
			"provider": "openai",
		},
		Requests: []BatchRequestItem{
			{
				CustomID: "chat-1",
				Method:   "POST",
				URL:      "/v1/chat/completions",
				Body:     json.RawMessage(`{"model":"smart","messages":[{"role":"user","content":"hi"}]}`),
				ExtraFields: UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					"x_item": json.RawMessage(`{"trace":true}`),
				}),
			},
		},
		ExtraFields: UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"x_top": json.RawMessage(`{"debug":true}`),
		}),
	}

	cloned := cloneBatchRequest(original)
	require.NotNil(t, cloned)

	cloned.Metadata["provider"] = "anthropic"
	cloned.Requests[0].CustomID = "chat-2"
	cloned.Requests[0].Body[10] = 'X'
	itemExtra := cloned.Requests[0].ExtraFields.Lookup("x_item")
	require.Greater(t, len(itemExtra), 9, "cloned item extra too short: %q", itemExtra)

	itemExtra[9] = 'f'
	topExtra := cloned.ExtraFields.Lookup("x_top")
	require.Greater(t, len(topExtra), 9, "cloned top extra too short: %q", topExtra)

	topExtra[9] = 'f'
	got := original.Metadata["provider"]
	require.Equal(t, "openai", got)
	got = original.Requests[0].CustomID
	require.Equal(t, "chat-1", got)
	got = string(original.Requests[0].Body)
	require.Equal(t, `{"model":"smart","messages":[{"role":"user","content":"hi"}]}`, got)
	got = string(original.Requests[0].ExtraFields.Lookup("x_item"))
	require.Equal(t, `{"trace":true}`, got)
	got = string(original.ExtraFields.Lookup("x_top"))
	require.Equal(t, `{"debug":true}`, got)
}

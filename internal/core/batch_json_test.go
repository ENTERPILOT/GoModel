package core

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBatchRequestJSON_PreservesUnknownFields(t *testing.T) {
	var req BatchRequest
	body := []byte(`{
		"input_file_id":"file-123",
		"endpoint":"/v1/chat/completions",
		"completion_window":"24h",
		"metadata":{"provider":"openai"},
		"requests":[{
			"custom_id":"chat-1",
			"method":"POST",
			"url":"/v1/chat/completions",
			"body":{"model":"gpt-5-mini","messages":[{"role":"user","content":"hi"}]},
			"x_item_flag":{"enabled":true,"label":"batch-item"}
		}],
		"x_top":{"trace":"batch-1","mode":"strict"}
	}`)
	err := json.Unmarshal(body, &req)
	require.NoError(t, err)
	require.NotNil(t, req.ExtraFields.Lookup("x_top"), "x_top missing from ExtraFields: %+v", req.ExtraFields)

	var topExtra map[string]any
	err = json.Unmarshal(req.ExtraFields.Lookup("x_top"), &topExtra)
	require.NoError(t, err)
	require.Equal(t, "batch-1", topExtra["trace"])
	require.Equal(t, "strict", topExtra["mode"], "x_top = %#v, want trace=batch-1 mode=strict", topExtra)
	require.Len(t, req.Requests, 1)
	require.NotNil(t, req.Requests[0].ExtraFields.Lookup("x_item_flag"), "x_item_flag missing from Requests[0].ExtraFields: %+v", req.Requests[0].ExtraFields)

	var itemExtra map[string]any
	err = json.Unmarshal(req.Requests[0].ExtraFields.Lookup("x_item_flag"), &itemExtra)
	require.NoError(t, err)
	require.Equal(t, true, itemExtra["enabled"])
	require.Equal(t, "batch-item", itemExtra["label"], "x_item_flag = %#v, want enabled=true label=batch-item", itemExtra)

	roundTrip, err := json.Marshal(req)
	require.NoError(t, err)

	var decoded map[string]any
	err = json.Unmarshal(roundTrip, &decoded)
	require.NoError(t, err)

	top, ok := decoded["x_top"].(map[string]any)
	require.True(t, ok, "x_top = %#v, want object", decoded["x_top"])
	require.Equal(t, "batch-1", top["trace"])
	require.Equal(t, "strict", top["mode"], "x_top = %#v, want trace=batch-1 mode=strict", top)

	requests := decoded["requests"].([]any)
	first := requests[0].(map[string]any)
	item, ok := first["x_item_flag"].(map[string]any)
	require.True(t, ok, "x_item_flag = %#v, want object", first["x_item_flag"])
	require.Equal(t, true, item["enabled"])
	require.Equal(t, "batch-item", item["label"], "x_item_flag = %#v, want enabled=true label=batch-item", item)
}

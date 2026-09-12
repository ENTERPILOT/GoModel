package core

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMessageUnmarshalJSON_StringContent(t *testing.T) {
	var msg Message
	err := json.Unmarshal([]byte(`{"role":"user","content":"hello"}`), &msg)
	require.NoError(t, err)
	require.Equal(t, "user", msg.Role)
	require.Equal(t, "hello", msg.Content)
}

func TestMessageUnmarshalJSON_MultimodalContent(t *testing.T) {
	var msg Message
	err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"text","text":"Describe this image"},{"type":"image_url","image_url":{"url":"https://example.com/image.png","detail":"high","media_type":"image/png"}}]}`), &msg)
	require.NoError(t, err)

	parts, ok := msg.Content.([]ContentPart)
	require.True(t, ok, "Content type = %T, want []ContentPart", msg.Content)
	require.Len(t, parts, 2)
	require.Equal(t, "text", parts[0].Type)
	require.Equal(t, "Describe this image", parts[0].Text, "unexpected first part: %+v", parts[0])
	require.Equal(t, "image_url", parts[1].Type)
	require.NotNil(t, parts[1].ImageURL)
	require.Equal(t, "https://example.com/image.png", parts[1].ImageURL.URL, "unexpected second part: %+v", parts[1])
	require.Equal(t, "image/png", parts[1].ImageURL.MediaType)
}

func TestMessageUnmarshalJSON_NullContentPreservedAsNil(t *testing.T) {
	var msg Message
	err := json.Unmarshal([]byte(`{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}`), &msg)
	require.NoError(t, err)
	require.Nil(t, msg.Content)
}

func TestMessageUnmarshalJSON_RejectsUnsupportedContentTypes(t *testing.T) {
	tests := []string{
		`{"role":"user","content":123}`,
		`{"role":"user","content":{"foo":"bar"}}`,
		`{"role":"user","content":[{"type":"unknown"}]}`,
	}

	for _, payload := range tests {
		t.Run(payload, func(t *testing.T) {
			var msg Message
			err := json.Unmarshal([]byte(payload), &msg)
			require.Error(t, err)
			require.True(t, strings.Contains(err.Error(), "content") || strings.Contains(err.Error(), "must be a string or array of content parts"), "error = %v, want content validation error", err)
		})
	}
}

func TestMessageMarshalJSON_RejectsUnsupportedContentType(t *testing.T) {
	_, err := json.Marshal(Message{Role: "user", Content: 123})
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be a string or array of content parts")
}

func TestMessageMarshalJSON_PreservesNullContentForToolCalls(t *testing.T) {
	body, err := json.Marshal(Message{
		Role:    "assistant",
		Content: nil,
		ToolCalls: []ToolCall{
			{
				ID:   "call_123",
				Type: "function",
				Function: FunctionCall{
					Name:      "lookup",
					Arguments: "{}",
				},
			},
		},
	})
	require.NoError(t, err)
	require.Contains(t, string(body), `"content":null`)
}

func TestMessageMarshalJSON_PreservesNullContentForToolCallsWhenContentIsEmptyString(t *testing.T) {
	body, err := json.Marshal(Message{
		Role:    "assistant",
		Content: "",
		ToolCalls: []ToolCall{
			{
				ID:   "call_123",
				Type: "function",
				Function: FunctionCall{
					Name:      "lookup",
					Arguments: "{}",
				},
			},
		},
	})
	require.NoError(t, err)
	require.Contains(t, string(body), `"content":null`)
}

func TestResponseMessageMarshalJSON_PreservesNullContentForToolCalls(t *testing.T) {
	body, err := json.Marshal(ResponseMessage{
		Role:    "assistant",
		Content: nil,
		ToolCalls: []ToolCall{
			{
				ID:   "call_123",
				Type: "function",
				Function: FunctionCall{
					Name:      "lookup",
					Arguments: "{}",
				},
			},
		},
	})
	require.NoError(t, err)
	require.Contains(t, string(body), `"content":null`)
}

func TestResponseMessageUnmarshalJSON_PreservesNullContentForToolCalls(t *testing.T) {
	var msg ResponseMessage
	err := json.Unmarshal([]byte(`{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}`), &msg)
	require.NoError(t, err)
	require.Nil(t, msg.Content)
}

func TestNormalizeMessageContent_RejectsEmptyTypedTextPart(t *testing.T) {
	_, err := NormalizeMessageContent([]ContentPart{{Type: "text", Text: ""}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "text part is missing text")
}

func TestMessageUnmarshalJSON_RejectsEmptyJSONTextPart(t *testing.T) {
	var msg Message
	err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"text","text":""}]}`), &msg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "text part is missing text")
}

func TestNormalizeMessageContent_RejectsEmptyMapTextPart(t *testing.T) {
	_, err := NormalizeMessageContent([]any{
		map[string]any{
			"type": "text",
			"text": "",
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "text part is missing text")
}

func TestMessageUnmarshalJSON_InputAudioContent(t *testing.T) {
	var msg Message
	err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"base64data","format":"wav"}}]}`), &msg)
	require.NoError(t, err)

	parts, ok := msg.Content.([]ContentPart)
	require.True(t, ok, "Content type = %T, want []ContentPart", msg.Content)
	require.Len(t, parts, 1)
	require.Equal(t, "input_audio", parts[0].Type)
	require.NotNil(t, parts[0].InputAudio)
	require.Equal(t, "base64data", parts[0].InputAudio.Data)
	require.Equal(t, "wav", parts[0].InputAudio.Format)
}

func TestMessageUnmarshalJSON_RejectsInputAudioMissingData(t *testing.T) {
	var msg Message
	err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"","format":"wav"}}]}`), &msg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "input_audio part is missing data or format")
}

func TestMessageUnmarshalJSON_RejectsInputAudioMissingFormat(t *testing.T) {
	var msg Message
	err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"abc","format":""}}]}`), &msg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "input_audio part is missing data or format")
}

func TestMessageUnmarshalJSON_AcceptsInputAudioDataURIWithoutFormat(t *testing.T) {
	var msg Message
	err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"data:audio/wav;base64,UklGRg=="}}]}`), &msg)
	require.NoError(t, err)

	parts, ok := msg.Content.([]ContentPart)
	require.True(t, ok)
	require.Len(t, parts, 1)
	require.NotNil(t, parts[0].InputAudio, "unexpected content: %+v", msg.Content)
	require.Equal(t, "data:audio/wav;base64,UklGRg==", parts[0].InputAudio.Data)
	require.Empty(t, parts[0].InputAudio.Format, "InputAudio = %+v, want data URI with empty format", parts[0].InputAudio)

	// Re-marshaling must preserve the wire shape: no synthesized format field.
	out, err := json.Marshal(msg)
	require.NoError(t, err)
	require.False(t, strings.Contains(string(out), `"format"`), "marshaled message should not contain format, got: %s", out)
}

func TestMessageUnmarshalJSON_RejectsInputAudioDataURIWithoutMediaType(t *testing.T) {
	// format omitted AND the data: URI carries no "type/subtype" media type.
	for _, data := range []string{"data:", "data:,UklGRg==", "data:base64,UklGRg==", "notdata:audio/wav,UklGRg=="} {
		body := `{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"` + data + `"}}]}`
		var msg Message
		err := json.Unmarshal([]byte(body), &msg)
		require.Error(t, err, "data %q", data)
		require.Contains(t, err.Error(), "input_audio part is missing data or format", "data %q", data)
	}
}

func TestMessageUnmarshalJSON_RejectsInputAudioNull(t *testing.T) {
	var msg Message
	err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"input_audio","input_audio":null}]}`), &msg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "input_audio part is missing data or format")
}

func TestMessageUnmarshalJSON_RejectsInputAudioNotObject(t *testing.T) {
	var msg Message
	err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"input_audio","input_audio":"string"}]}`), &msg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "input_audio must be an object")
}

func TestNormalizeMessageContent_InputAudioTypedPart(t *testing.T) {
	result, err := NormalizeMessageContent([]ContentPart{{
		Type:       "input_audio",
		InputAudio: &InputAudioContent{Data: "abc", Format: "wav"},
	}})
	require.NoError(t, err)

	parts, ok := result.([]ContentPart)
	require.True(t, ok, "result type = %T, want []ContentPart", result)
	require.Len(t, parts, 1)
	require.Equal(t, "input_audio", parts[0].Type)
	require.NotNil(t, parts[0].InputAudio, "unexpected part: %+v", parts[0])
	require.Equal(t, "abc", parts[0].InputAudio.Data)
	require.Equal(t, "wav", parts[0].InputAudio.Format, "InputAudio = %+v, want {abc wav}", parts[0].InputAudio)
}

func TestNormalizeMessageContent_RejectsNilInputAudio(t *testing.T) {
	_, err := NormalizeMessageContent([]ContentPart{{Type: "input_audio", InputAudio: nil}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "input_audio part is missing data or format")
}

func TestNormalizeMessageContent_InputAudioFromMap(t *testing.T) {
	result, err := NormalizeMessageContent([]any{
		map[string]any{
			"type":        "input_audio",
			"input_audio": map[string]any{"data": "abc", "format": "wav"},
		},
	})
	require.NoError(t, err)

	parts, ok := result.([]ContentPart)
	require.True(t, ok, "result type = %T, want []ContentPart", result)
	require.Len(t, parts, 1)
	require.Equal(t, "input_audio", parts[0].Type)
	require.NotNil(t, parts[0].InputAudio, "unexpected part: %+v", parts[0])
	require.Equal(t, "abc", parts[0].InputAudio.Data)
	require.Equal(t, "wav", parts[0].InputAudio.Format, "InputAudio = %+v, want {abc wav}", parts[0].InputAudio)
}

func TestNormalizeMessageContent_RejectsInputAudioFromMapMissingFields(t *testing.T) {
	_, err := NormalizeMessageContent([]any{
		map[string]any{
			"type":        "input_audio",
			"input_audio": map[string]any{"data": "", "format": "wav"},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "input_audio part is missing data or format")
}

func TestMessageUnmarshalJSON_MixedTextImageAudio(t *testing.T) {
	var msg Message
	err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"text","text":"Describe"},{"type":"image_url","image_url":{"url":"https://example.com/img.png"}},{"type":"input_audio","input_audio":{"data":"abc","format":"mp3"}}]}`), &msg)
	require.NoError(t, err)

	parts, ok := msg.Content.([]ContentPart)
	require.True(t, ok, "Content type = %T, want []ContentPart", msg.Content)
	require.Len(t, parts, 3)
	require.Equal(t, "text", parts[0].Type)
	require.Equal(t, "Describe", parts[0].Text, "unexpected part 0: %+v", parts[0])
	require.Equal(t, "image_url", parts[1].Type)
	require.NotNil(t, parts[1].ImageURL)
	require.Equal(t, "https://example.com/img.png", parts[1].ImageURL.URL, "unexpected part 1: %+v", parts[1])
	require.Equal(t, "input_audio", parts[2].Type)
	require.NotNil(t, parts[2].InputAudio)
	require.Equal(t, "abc", parts[2].InputAudio.Data)
	require.Equal(t, "mp3", parts[2].InputAudio.Format, "unexpected part 2: %+v", parts[2])
}

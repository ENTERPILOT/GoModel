package gemini

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

func TestNativeChatResponse_SurfacesInlineImages(t *testing.T) {
	var geminiResp geminiGenerateContentResponse
	err := json.Unmarshal([]byte(`{"candidates":[{"content":{"role":"model","parts":[
		{"text":"Here is an image: "},
		{"inlineData":{"mimeType":"image/png","data":"aW1n"}}
	]},"finishReason":"STOP"}]}`), &geminiResp)
	require.NoError(t, err)

	resp, err := nativeChatResponse(&core.ChatRequest{Model: "gemini-2.5-flash-image"}, &geminiResp, "gemini")
	require.NoError(t, err)
	require.Len(t, resp.Choices, 1)
	encoded, err := json.Marshal(resp.Choices[0].Message)
	require.NoError(t, err)
	assert.JSONEq(t, `{"role":"assistant","content":"Here is an image: ",
		"images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1n"}}]}`, string(encoded))

	output := providers.ConvertChatResponseToResponses(resp).Output
	require.Len(t, output, 2)
	assert.Equal(t, "message", output[0].Type)
	require.Len(t, output[0].Content, 1)
	assert.Equal(t, "output_text", output[0].Content[0].Type)
	encoded, err = json.Marshal(output[1])
	require.NoError(t, err)
	assert.JSONEq(t, `{"id":"`+output[1].ID+`","type":"image_generation_call","status":"completed",
		"result":"aW1n","output_format":"png"}`, string(encoded))
}

func TestNativeChatResponse_ImageOnlyResponsesOutput(t *testing.T) {
	var geminiResp geminiGenerateContentResponse
	err := json.Unmarshal([]byte(`{"candidates":[{"content":{"role":"model","parts":[
		{"inlineData":{"mimeType":"image/png","data":"aW1n"}}
	]},"finishReason":"STOP"}]}`), &geminiResp)
	require.NoError(t, err)

	resp, err := nativeChatResponse(&core.ChatRequest{Model: "gemini-2.5-flash-image"}, &geminiResp, "gemini")
	require.NoError(t, err)
	require.Len(t, resp.Choices, 1)
	assert.Empty(t, resp.Choices[0].Message.Content)

	output := providers.ConvertChatResponseToResponses(resp).Output
	require.Len(t, output, 1)
	assert.Equal(t, "image_generation_call", output[0].Type)
}

func TestNativeChatResponse_TextOnlyStaysString(t *testing.T) {
	var geminiResp geminiGenerateContentResponse
	err := json.Unmarshal([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}]}`), &geminiResp)
	require.NoError(t, err)

	resp, err := nativeChatResponse(&core.ChatRequest{Model: "gemini-2.5-flash"}, &geminiResp, "gemini")
	require.NoError(t, err)
	require.Len(t, resp.Choices, 1)
	assert.Equal(t, "hi", resp.Choices[0].Message.Content)
}

func TestNativeChatResponse_ImageThoughtSignaturesRoundTrip(t *testing.T) {
	var geminiResp geminiGenerateContentResponse
	err := json.Unmarshal([]byte(`{"candidates":[{"content":{"role":"model","parts":[
		{"inlineData":{"mimeType":"image/png","data":"aW1n"},"thoughtSignature":"sig-a"},
		{"inlineData":{"mimeType":"image/jpeg","data":"anBn"},"thoughtSignature":"sig-b"}
	]},"finishReason":"STOP"}]}`), &geminiResp)
	require.NoError(t, err)

	resp, err := nativeChatResponse(&core.ChatRequest{Model: "gemini-3-pro-image-preview"}, &geminiResp, "gemini")
	require.NoError(t, err)
	require.Len(t, resp.Choices, 1)

	// Echo the assistant turn back through the public JSON shape.
	encoded, err := json.Marshal(resp.Choices[0].Message)
	require.NoError(t, err)
	var assistant core.Message
	require.NoError(t, json.Unmarshal(encoded, &assistant))

	req, err := convertChatRequestToGemini(&core.ChatRequest{
		Model: "gemini-3-pro-image-preview",
		Messages: []core.Message{
			{Role: "user", Content: "draw two"},
			assistant,
			{Role: "user", Content: "make them blue"},
		},
	})
	require.NoError(t, err)
	require.Len(t, req.Contents, 3)
	parts := req.Contents[1].Parts
	require.Len(t, parts, 2)
	require.NotNil(t, parts[0].InlineData)
	require.NotNil(t, parts[1].InlineData)
	assert.Equal(t, "image/png", parts[0].InlineData.MimeType)
	assert.Equal(t, "aW1n", parts[0].InlineData.Data)
	assert.Equal(t, "sig-a", parts[0].ThoughtSignature)
	assert.Equal(t, "anBn", parts[1].InlineData.Data)
	assert.Equal(t, "sig-b", parts[1].ThoughtSignature)
}

func TestNativeChatResponse_ImageThenSignedTextKeepsEachSignature(t *testing.T) {
	var geminiResp geminiGenerateContentResponse
	err := json.Unmarshal([]byte(`{"candidates":[{"content":{"role":"model","parts":[
		{"inlineData":{"mimeType":"image/png","data":"aW1n"},"thoughtSignature":"sig-image"},
		{"text":"Here it is.","thoughtSignature":"sig-text"}
	]},"finishReason":"STOP"}]}`), &geminiResp)
	require.NoError(t, err)

	resp, err := nativeChatResponse(&core.ChatRequest{Model: "gemini-3-pro-image-preview"}, &geminiResp, "gemini")
	require.NoError(t, err)
	require.Len(t, resp.Choices, 1)

	encoded, err := json.Marshal(resp.Choices[0].Message)
	require.NoError(t, err)
	var assistant core.Message
	require.NoError(t, json.Unmarshal(encoded, &assistant))

	req, err := convertChatRequestToGemini(&core.ChatRequest{
		Model:    "gemini-3-pro-image-preview",
		Messages: []core.Message{{Role: "user", Content: "draw"}, assistant, {Role: "user", Content: "again"}},
	})
	require.NoError(t, err)
	require.Len(t, req.Contents, 3)
	parts := req.Contents[1].Parts
	require.Len(t, parts, 2)
	assert.Equal(t, "Here it is.", parts[0].Text)
	assert.Equal(t, "sig-text", parts[0].ThoughtSignature)
	require.NotNil(t, parts[1].InlineData)
	assert.Equal(t, "aW1n", parts[1].InlineData.Data)
	assert.Equal(t, "sig-image", parts[1].ThoughtSignature)
}

func TestNativeChatResponse_ImageSignatureRoundTripsThroughResponses(t *testing.T) {
	var geminiResp geminiGenerateContentResponse
	err := json.Unmarshal([]byte(`{"candidates":[{"content":{"role":"model","parts":[
		{"inlineData":{"mimeType":"image/png","data":"aW1n"},"thoughtSignature":"sig-image"},
		{"text":"Here it is.","thoughtSignature":"sig-text"}
	]},"finishReason":"STOP"}]}`), &geminiResp)
	require.NoError(t, err)

	resp, err := nativeChatResponse(&core.ChatRequest{Model: "gemini-3-pro-image-preview"}, &geminiResp, "gemini")
	require.NoError(t, err)

	// Echo the Responses output item back as Responses input.
	output := providers.ConvertChatResponseToResponses(resp).Output
	encoded, err := json.Marshal(output)
	require.NoError(t, err)
	var items []any
	require.NoError(t, json.Unmarshal(encoded, &items))
	input := append([]any{map[string]any{"role": "user", "content": "draw"}}, items...)
	input = append(input, map[string]any{"role": "user", "content": "again"})

	// Decode the request the way the HTTP handler does.
	body, err := json.Marshal(map[string]any{"model": "gemini-3-pro-image-preview", "input": input})
	require.NoError(t, err)
	var responsesReq core.ResponsesRequest
	require.NoError(t, json.Unmarshal(body, &responsesReq))
	chatReq, err := providers.ConvertResponsesRequestToChat(&responsesReq)
	require.NoError(t, err)
	req, err := convertChatRequestToGemini(chatReq)
	require.NoError(t, err)
	require.Len(t, req.Contents, 3)
	parts := req.Contents[1].Parts
	require.Len(t, parts, 2)
	assert.Equal(t, "Here it is.", parts[0].Text)
	assert.Equal(t, "sig-text", parts[0].ThoughtSignature)
	require.NotNil(t, parts[1].InlineData)
	assert.Equal(t, "aW1n", parts[1].InlineData.Data)
	assert.Equal(t, "sig-image", parts[1].ThoughtSignature)
}

func TestConvertResponsesInput_SkipsImageWithoutResult(t *testing.T) {
	messages, err := providers.ConvertResponsesInputToMessages([]any{
		map[string]any{"role": "user", "content": "draw"},
		map[string]any{"type": "image_generation_call", "status": "failed"},
		map[string]any{"role": "user", "content": "again"},
	})
	require.NoError(t, err)
	require.Len(t, messages, 2)
	assert.Equal(t, "user", messages[0].Role)
	assert.Equal(t, "user", messages[1].Role)
}

func TestConvertResponsesInput_RejectsMalformedImageGenerationCall(t *testing.T) {
	tests := []struct {
		name string
		item map[string]any
	}{
		{name: "result object", item: map[string]any{"type": "image_generation_call", "result": map[string]any{}}},
		{name: "output_format object", item: map[string]any{"type": "image_generation_call", "result": "aW1n", "output_format": map[string]any{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := providers.ConvertResponsesInputToMessages([]any{
				map[string]any{"role": "user", "content": "draw"},
				tt.item,
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "index 1: image_generation_call")
		})
	}
}

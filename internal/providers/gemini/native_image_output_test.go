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
	parts, ok := resp.Choices[0].Message.Content.([]core.ContentPart)
	require.True(t, ok, "content should be parts, got %T", resp.Choices[0].Message.Content)
	require.Len(t, parts, 2)
	assert.Equal(t, core.ContentPart{Type: "text", Text: "Here is an image: "}, parts[0])
	require.NotNil(t, parts[1].ImageURL)
	assert.Equal(t, "image_url", parts[1].Type)
	assert.Equal(t, "data:image/png;base64,aW1n", parts[1].ImageURL.URL)

	output := providers.ConvertChatResponseToResponses(resp).Output
	require.Len(t, output, 1)
	require.Len(t, output[0].Content, 2)
	require.NotNil(t, output[0].Content[1].ImageURL)
	assert.Equal(t, "data:image/png;base64,aW1n", output[0].Content[1].ImageURL.URL)
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

	chatReq, err := providers.ConvertResponsesRequestToChat(&core.ResponsesRequest{Model: "gemini-3-pro-image-preview", Input: input})
	require.NoError(t, err)
	req, err := convertChatRequestToGemini(chatReq)
	require.NoError(t, err)
	require.Len(t, req.Contents, 3)
	parts := req.Contents[1].Parts
	require.Len(t, parts, 2)
	assert.Equal(t, "Here it is.", parts[0].Text)
	assert.Equal(t, "sig-text", parts[0].ThoughtSignature)
	require.NotNil(t, parts[1].InlineData)
	assert.Equal(t, "sig-image", parts[1].ThoughtSignature)
}

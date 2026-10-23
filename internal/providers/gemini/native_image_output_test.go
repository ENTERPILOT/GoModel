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

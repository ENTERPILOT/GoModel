package bedrock

import (
	"testing"

	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func imagePart(url string) core.ContentPart {
	return core.ContentPart{Type: "image_url", ImageURL: &core.ImageURLContent{URL: url}}
}

func TestConvertMessages_ForwardsDataURLImages(t *testing.T) {
	content := []core.ContentPart{
		{Type: "text", Text: "what is this?"},
		imagePart("data:image/png;base64,aGVsbG8="),
	}
	_, messages, err := convertMessages([]core.Message{
		{Role: "user", Content: content},
		{Role: "assistant", ToolCalls: []core.ToolCall{{ID: "call_1", Type: "function", Function: core.FunctionCall{Name: "shot", Arguments: "{}"}}}},
		{Role: "tool", ToolCallID: "call_1", Content: []core.ContentPart{imagePart("data:image/jpeg;base64,aGVsbG8=")}},
	})
	require.NoError(t, err)
	require.Len(t, messages, 3)

	require.Len(t, messages[0].Content, 2)
	text, ok := messages[0].Content[0].(*brtypes.ContentBlockMemberText)
	require.True(t, ok)
	assert.Equal(t, "what is this?", text.Value)
	image, ok := messages[0].Content[1].(*brtypes.ContentBlockMemberImage)
	require.True(t, ok)
	assert.Equal(t, brtypes.ImageFormatPng, image.Value.Format)
	source, ok := image.Value.Source.(*brtypes.ImageSourceMemberBytes)
	require.True(t, ok)
	assert.Equal(t, []byte("hello"), source.Value)

	result, ok := messages[2].Content[0].(*brtypes.ContentBlockMemberToolResult)
	require.True(t, ok)
	require.Len(t, result.Value.Content, 1)
	toolImage, ok := result.Value.Content[0].(*brtypes.ToolResultContentBlockMemberImage)
	require.True(t, ok)
	assert.Equal(t, brtypes.ImageFormatJpeg, toolImage.Value.Format)
}

func TestConvertMessages_ImageOnlyUserMessageIsKept(t *testing.T) {
	// The []any form is what dynamically decoded requests carry.
	_, messages, err := convertMessages([]core.Message{{Role: "user", Content: []any{
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/webp;base64,aGVsbG8="}},
	}}})
	require.NoError(t, err)
	require.Len(t, messages, 1)
	require.Len(t, messages[0].Content, 1)
	_, ok := messages[0].Content[0].(*brtypes.ContentBlockMemberImage)
	require.True(t, ok)
}

func TestConvertMessages_TextPartsStayOneBlock(t *testing.T) {
	_, messages, err := convertMessages([]core.Message{{Role: "user", Content: []core.ContentPart{
		{Type: "text", Text: "a"}, {Type: "text", Text: "b"},
	}}})
	require.NoError(t, err)
	require.Len(t, messages, 1)
	require.Len(t, messages[0].Content, 1)
}

func TestConvertMessages_RejectsContentItCannotForward(t *testing.T) {
	tests := map[string]core.ContentPart{
		"remote image url": imagePart("https://example.com/cat.png"),
		"unsupported type": imagePart("data:image/tiff;base64,aGVsbG8="),
		"not base64":       imagePart("data:image/png,hello"),
		"bad base64":       imagePart("data:image/png;base64,%%%"),
		"empty data":       imagePart("data:image/png;base64,"),
		"file":             {Type: "file", File: &core.FileContent{FileData: "data:application/pdf;base64,aGVsbG8="}},
		"input_audio":      {Type: "input_audio", InputAudio: &core.InputAudioContent{Data: "aGVsbG8=", Format: "wav"}},
		"video_url":        {Type: "video_url", VideoURL: &core.VideoURLContent{URL: "https://example.com/a.mp4"}},
	}
	for name, part := range tests {
		for _, role := range []string{"user", "tool"} {
			t.Run(role+" "+name, func(t *testing.T) {
				_, _, err := convertMessages([]core.Message{{Role: role, ToolCallID: "call_1", Content: []core.ContentPart{{Type: "text", Text: "look"}, part}}})
				var gatewayErr *core.GatewayError
				require.ErrorAs(t, err, &gatewayErr)
				assert.Equal(t, 400, gatewayErr.HTTPStatusCode())
				assert.Contains(t, gatewayErr.Message, "bedrock")
			})
		}
	}
}

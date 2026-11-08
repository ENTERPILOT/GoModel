package providers

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// convertTestStream runs a chat stream through the Responses converter and
// returns the item of every output_item.added and output_item.done event plus
// the terminal response object.
func convertTestStream(t *testing.T, stream string) (added, done []map[string]any, terminal map[string]any) {
	t.Helper()

	converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(stream)), "test-model", "gemini")
	raw, err := io.ReadAll(converter)
	require.NoError(t, err)

	for _, event := range parseTestSSEEvents(t, string(raw)) {
		if event.Done {
			continue
		}
		switch event.Name {
		case "response.output_item.added":
			item, _ := event.Payload["item"].(map[string]any)
			item["output_index"] = event.Payload["output_index"]
			added = append(added, item)
		case "response.output_item.done":
			item, _ := event.Payload["item"].(map[string]any)
			item["output_index"] = event.Payload["output_index"]
			done = append(done, item)
		case "response.completed", "response.incomplete":
			terminal, _ = event.Payload["response"].(map[string]any)
		}
	}
	require.NotNil(t, terminal, "stream %s", raw)
	return added, done, terminal
}

func outputItems(t *testing.T, response map[string]any) []map[string]any {
	t.Helper()

	raw, ok := response["output"].([]any)
	require.True(t, ok, "output = %#v", response["output"])
	items := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		item, ok := entry.(map[string]any)
		require.True(t, ok, "output item = %#v", entry)
		items = append(items, item)
	}
	return items
}

func TestOpenAIResponsesStreamConverter_GeneratedImages(t *testing.T) {
	stream := `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"Here is a cat: "},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1n","extra_content":{"google":{"thought_signature":"sig-a"}}}}]},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]
`
	added, done, completed := convertTestStream(t, stream)

	assert.Equal(t, "completed", completed["status"])
	output := outputItems(t, completed)
	require.Len(t, output, 2)
	assert.Equal(t, "message", output[0]["type"])
	image := output[1]
	assert.Equal(t, "image_generation_call", image["type"])
	assert.Equal(t, "completed", image["status"])
	assert.Equal(t, "aW1n", image["result"])
	assert.Equal(t, "png", image["output_format"])
	assert.Equal(t, map[string]any{"google": map[string]any{"thought_signature": "sig-a"}}, image["extra_content"])
	assert.True(t, strings.HasPrefix(image["id"].(string), "ig_"), "id = %v", image["id"])

	require.Len(t, added, 2)
	assert.Equal(t, "image_generation_call", added[1]["type"])
	assert.Equal(t, "in_progress", added[1]["status"])
	assert.Equal(t, image["id"], added[1]["id"])
	assert.InDelta(t, 1, added[1]["output_index"], 0)
	assert.NotContains(t, added[1], "result")

	require.Len(t, done, 2)
	var imageDone map[string]any
	for _, item := range done {
		if item["type"] == "image_generation_call" {
			imageDone = item
		}
	}
	require.NotNil(t, imageDone)
	assert.InDelta(t, 1, imageDone["output_index"], 0)
	assert.Equal(t, "aW1n", imageDone["result"])
}

func TestOpenAIResponsesStreamConverter_ImageOnlyStream(t *testing.T) {
	stream := `data: {"choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,b25l"}},{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,dHdv"}}]},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]
`
	added, done, completed := convertTestStream(t, stream)

	output := outputItems(t, completed)
	require.Len(t, output, 2, "an image-only turn has no empty message item")
	assert.Equal(t, "b25l", output[0]["result"])
	assert.Equal(t, "png", output[0]["output_format"])
	assert.Equal(t, "dHdv", output[1]["result"])
	assert.Equal(t, "jpeg", output[1]["output_format"])
	assert.NotEqual(t, output[0]["id"], output[1]["id"])
	require.Len(t, added, 2)
	require.Len(t, done, 2)
	assert.InDelta(t, 0, done[0]["output_index"], 0)
	assert.InDelta(t, 1, done[1]["output_index"], 0)
}

func TestOpenAIResponsesStreamConverter_ImageAfterReasoningClosesReasoning(t *testing.T) {
	stream := `data: {"choices":[{"index":0,"delta":{"reasoning_content":"thinking"},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1n"}}]},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]
`
	_, done, completed := convertTestStream(t, stream)

	require.Len(t, done, 2)
	assert.Equal(t, "reasoning", done[0]["type"], "reasoning closes before the image item")
	assert.Equal(t, "image_generation_call", done[1]["type"])
	output := outputItems(t, completed)
	require.Len(t, output, 2)
	assert.Equal(t, "reasoning", output[0]["type"])
	assert.Equal(t, "image_generation_call", output[1]["type"])
}

func TestOpenAIResponsesStreamConverter_SkipsUnusableImages(t *testing.T) {
	tests := []struct {
		name   string
		images string
	}{
		{name: "remote url", images: `[{"type":"image_url","image_url":{"url":"https://example.com/cat.png"}}]`},
		{name: "empty data", images: `[{"type":"image_url","image_url":{"url":"data:image/png;base64,"}}]`},
		{name: "no image_url", images: `[{"type":"text","text":"not an image"}]`},
		{name: "null", images: `null`},
		{name: "not an array", images: `{"url":"data:image/png;base64,aW1n"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream := `data: {"choices":[{"index":0,"delta":{"content":"hi","images":` + tt.images + `},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]
`
			_, _, completed := convertTestStream(t, stream)

			assert.Equal(t, "completed", completed["status"])
			output := outputItems(t, completed)
			require.Len(t, output, 1)
			assert.Equal(t, "message", output[0]["type"])
		})
	}
}

func TestOpenAIResponsesStreamConverter_ImagesOnTolerantPath(t *testing.T) {
	// The off-spec content array forces the tolerant decode; the image in
	// the same delta must still come through.
	stream := `data: {"choices":[{"index":0,"delta":{"content":[{"type":"text","text":"x"}],"images":[{"type":"image_url","image_url":{"url":"data:image/webp;base64,aW1n"}}]},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]
`
	_, _, completed := convertTestStream(t, stream)

	output := outputItems(t, completed)
	require.Len(t, output, 1)
	assert.Equal(t, "image_generation_call", output[0]["type"])
	assert.Equal(t, "webp", output[0]["output_format"])
}

func TestOpenAIResponsesStreamConverter_ImageInInterruptedStream(t *testing.T) {
	// No finish_reason and no [DONE]: the image already delivered stays
	// completed while the response itself ends incomplete.
	stream := `data: {"choices":[{"index":0,"delta":{"images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1n"}}]},"finish_reason":null}]}

`
	_, _, terminal := convertTestStream(t, stream)

	assert.Equal(t, "incomplete", terminal["status"])
	output := outputItems(t, terminal)
	require.Len(t, output, 1)
	assert.Equal(t, "completed", output[0]["status"])
	assert.Equal(t, "aW1n", output[0]["result"])
}

func TestOpenAIResponsesStreamConverter_LateReasoningAfterImageKeepsIndexes(t *testing.T) {
	stream := `data: {"choices":[{"index":0,"delta":{"images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1n"}}]},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"reasoning_content":"late"},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}

data: [DONE]
`
	added, _, completed := convertTestStream(t, stream)

	indexes := map[float64]bool{}
	for _, item := range added {
		index, ok := item["output_index"].(float64)
		require.True(t, ok, "output_index = %#v", item["output_index"])
		assert.False(t, indexes[index], "duplicate output_index %v", index)
		indexes[index] = true
		assert.NotEqual(t, "reasoning", item["type"], "late reasoning must be dropped")
	}
	output := outputItems(t, completed)
	require.Len(t, output, 2)
	assert.Equal(t, "image_generation_call", output[0]["type"])
	assert.Equal(t, "message", output[1]["type"])
}

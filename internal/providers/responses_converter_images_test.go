package providers

import (
	"bufio"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
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

func TestOpenAIResponsesStreamConverter_MalformedEntryKeepsValidImages(t *testing.T) {
	images := `[{"type":"image_url","image_url":{"url":"data:image/png;base64,b25l"}},` +
		`{"type":"image_url"},42,"str",null,{"type":"image_url","image_url":"x"},` +
		`{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,dHdv"}}]`
	for _, tolerant := range []bool{false, true} {
		content := `"hi"`
		if tolerant {
			content = `[{"type":"text","text":"hi"}]`
		}
		stream := `data: {"choices":[{"index":0,"delta":{"content":` + content + `,"images":` + images + `},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]
`
		_, _, completed := convertTestStream(t, stream)

		var results []any
		for _, item := range outputItems(t, completed) {
			if item["type"] == "image_generation_call" {
				results = append(results, item["result"])
			}
		}
		assert.Equal(t, []any{"b25l", "dHdv"}, results, "tolerant=%v", tolerant)
	}
}

// A large image line arrives over many small reads. Lines split at every
// byte must still be found once each, in order, with nothing lost or merged.
func TestOpenAIResponsesStreamConverter_LinesSplitAcrossReads(t *testing.T) {
	img := strings.Repeat("QUJD", 16<<10)
	stream := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"before\"},\"finish_reason\":null}]}\r\n\r\n" +
		`data: {"choices":[{"index":0,"delta":{"images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + img + `"}}]},"finish_reason":null}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{"content":"after"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"

	converter := NewOpenAIResponsesStreamConverter(io.NopCloser(iotest.OneByteReader(strings.NewReader(stream))), "test-model", "gemini")
	raw, err := io.ReadAll(converter)
	require.NoError(t, err)
	var completed map[string]any
	for _, event := range parseTestSSEEvents(t, string(raw)) {
		if event.Name == "response.completed" {
			completed, _ = event.Payload["response"].(map[string]any)
		}
	}
	require.NotNil(t, completed, "stream %s", raw)

	output := outputItems(t, completed)
	require.Len(t, output, 3)
	assert.Equal(t, "message", output[0]["type"])
	assert.Equal(t, "image_generation_call", output[1]["type"])
	assert.Equal(t, img, output[1]["result"])
	assert.Equal(t, "message", output[2]["type"])
}

// Readers such as bufio give up after 100 reads that return nothing, so the
// converter must not hand back empty reads while a large line is still
// arriving.
func TestOpenAIResponsesStreamConverter_LargeLineMakesProgressForBufio(t *testing.T) {
	img := strings.Repeat("QUJD", 75<<10)
	stream := `data: {"choices":[{"index":0,"delta":{"images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + img + `"}}]},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"

	converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(stream)), "test-model", "gemini")
	reader := bufio.NewReader(converter)
	var raw []byte
	for {
		line, err := reader.ReadBytes('\n')
		raw = append(raw, line...)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
	}
	assert.Contains(t, string(raw), `"result":"`+img+`"`)
	assert.Contains(t, string(raw), "response.completed")
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

func TestOpenAIResponsesStreamConverter_TextImageTextKeepsOrder(t *testing.T) {
	stream := `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"Before. "},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1n"}}]},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"content":"After."},"finish_reason":"stop"}]}

data: [DONE]
`
	converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(stream)), "test-model", "gemini")
	raw, err := io.ReadAll(converter)
	require.NoError(t, err)

	// Output items open and close one after another: an item may not be
	// added while a lower one is open, and nothing addresses a closed item.
	open, closed := map[float64]bool{}, map[float64]bool{}
	var terminal map[string]any
	for _, event := range parseTestSSEEvents(t, string(raw)) {
		if event.Done {
			continue
		}
		if event.Name == "response.completed" {
			terminal, _ = event.Payload["response"].(map[string]any)
			continue
		}
		index, ok := event.Payload["output_index"].(float64)
		if !ok {
			continue
		}
		assert.False(t, closed[index], "%s addresses closed item %v", event.Name, index)
		switch event.Name {
		case "response.output_item.added":
			assert.Empty(t, open, "item %v added while %v still open", index, open)
			open[index] = true
		case "response.output_item.done":
			delete(open, index)
			closed[index] = true
		}
	}
	require.NotNil(t, terminal, "stream %s", raw)

	output := outputItems(t, terminal)
	require.Len(t, output, 3)
	assert.Equal(t, "message", output[0]["type"])
	assert.Equal(t, "image_generation_call", output[1]["type"])
	assert.Equal(t, "message", output[2]["type"])
	assert.NotEqual(t, output[0]["id"], output[2]["id"])
	text := func(item map[string]any) any {
		content, _ := item["content"].([]any)
		require.Len(t, content, 1)
		part, _ := content[0].(map[string]any)
		return part["text"]
	}
	assert.Equal(t, "Before. ", text(output[0]))
	assert.Equal(t, "After.", text(output[2]))
}

// A thought signature belongs to the message item whose text it arrived with:
// text that follows an image opens a new item, and neither item may carry the
// other's signature.
func TestOpenAIResponsesStreamConverter_SignatureStaysOnItsMessageAcrossImage(t *testing.T) {
	const image = `{"choices":[{"index":0,"delta":{"images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1n"}}]},"finish_reason":null}]}`
	tests := []struct {
		name         string
		before       string
		after        string
		wantBefore   any
		wantAfter    any
		tolerantPath bool
	}{
		{
			name:      "signature after image",
			before:    `{"choices":[{"index":0,"delta":{"content":"A"},"finish_reason":null}]}`,
			after:     `{"choices":[{"index":0,"delta":{"content":"B","extra_content":{"google":{"thought_signature":"sig-b"}}},"finish_reason":"stop"}]}`,
			wantAfter: map[string]any{"google": map[string]any{"thought_signature": "sig-b"}},
		},
		{
			name:       "signature before image",
			before:     `{"choices":[{"index":0,"delta":{"content":"A","extra_content":{"google":{"thought_signature":"sig-a"}}},"finish_reason":null}]}`,
			after:      `{"choices":[{"index":0,"delta":{"content":"B"},"finish_reason":"stop"}]}`,
			wantBefore: map[string]any{"google": map[string]any{"thought_signature": "sig-a"}},
		},
		{
			name:      "signature after image on tolerant path",
			before:    `{"choices":[{"index":0,"delta":{"content":"A"},"finish_reason":null}]}`,
			after:     `{"choices":[{"index":0,"delta":{"content":"B","tool_calls":"off-spec","extra_content":{"google":{"thought_signature":"sig-b"}}},"finish_reason":"stop"}]}`,
			wantAfter: map[string]any{"google": map[string]any{"thought_signature": "sig-b"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream := "data: " + tt.before + "\n\ndata: " + image + "\n\ndata: " + tt.after + "\n\ndata: [DONE]\n\n"
			_, done, completed := convertTestStream(t, stream)

			output := outputItems(t, completed)
			require.Len(t, output, 3)
			assert.Equal(t, "image_generation_call", output[1]["type"])
			assert.Equal(t, tt.wantBefore, output[0]["extra_content"], "pre-image message")
			assert.Equal(t, tt.wantAfter, output[2]["extra_content"], "post-image message")
			for _, item := range done {
				if item["id"] == output[0]["id"] {
					assert.Equal(t, output[0]["extra_content"], item["extra_content"], "terminal item matches its output_item.done")
				}
			}
		})
	}
}

// The non-streaming path skips a malformed images entry the same way the
// stream does, instead of dropping every image of the message.
func TestBuildResponsesOutputItems_MalformedImageEntryKeepsValidImages(t *testing.T) {
	images := `[{"type":"image_url","image_url":{"url":"data:image/png;base64,b25l"}},{"type":"image_url"},42,` +
		`{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,dHdv"}}]`
	msg := core.ResponseMessage{
		Role:        "assistant",
		Content:     "",
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{core.ChatImagesField: json.RawMessage(images)}),
	}

	var results []string
	for _, item := range BuildResponsesOutputItems(msg) {
		if item.Type == "image_generation_call" {
			var result string
			require.NoError(t, json.Unmarshal(item.ExtraFields.Lookup("result"), &result))
			results = append(results, result)
		}
	}
	assert.Equal(t, []string{"b25l", "dHdv"}, results)
}

// Text after a tool call and an image goes in a new message item after them,
// even when the tool call, not the image, closed the first message.
func TestOpenAIResponsesStreamConverter_TextToolImageTextKeepsOrder(t *testing.T) {
	stream := `data: {"choices":[{"index":0,"delta":{"content":"before"},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"save","arguments":"{}"}}]},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1n"}}]},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"content":"after"},"finish_reason":"stop"}]}

data: [DONE]
`
	_, done, completed := convertTestStream(t, stream)

	output := outputItems(t, completed)
	require.Len(t, output, 4)
	var types []any
	for _, item := range output {
		types = append(types, item["type"])
	}
	assert.Equal(t, []any{"message", "function_call", "image_generation_call", "message"}, types)
	assert.Equal(t, "before", output[0]["content"].([]any)[0].(map[string]any)["text"])
	assert.Equal(t, "after", output[3]["content"].([]any)[0].(map[string]any)["text"])
	for _, item := range done {
		if item["type"] == "message" {
			assert.NotEqual(t, "beforeafter", item["content"].([]any)[0].(map[string]any)["text"])
		}
	}
}

// An image between argument deltas must not complete the tool call early:
// its later arguments still belong to it.
func TestOpenAIResponsesStreamConverter_ImageKeepsStreamingToolCallOpen(t *testing.T) {
	stream := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"save","arguments":"{\"a\""}}]},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1n"}}]},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":1}"}}]},"finish_reason":"tool_calls"}]}

data: [DONE]
`
	_, _, completed := convertTestStream(t, stream)

	var arguments []any
	for _, item := range outputItems(t, completed) {
		if item["type"] == "function_call" {
			arguments = append(arguments, item["arguments"])
		}
	}
	assert.Equal(t, []any{`{"a":1}`}, arguments)
}

// Without an image, text after a tool call keeps its existing handling: no
// extra message item for any provider.
func TestOpenAIResponsesStreamConverter_TextAfterToolCallWithoutImageUnchanged(t *testing.T) {
	stream := `data: {"choices":[{"index":0,"delta":{"content":"A"},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"save","arguments":"{}"}}]},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"content":"B"},"finish_reason":"tool_calls"}]}

data: [DONE]
`
	_, _, completed := convertTestStream(t, stream)

	var types []any
	for _, item := range outputItems(t, completed) {
		types = append(types, item["type"])
	}
	assert.Equal(t, []any{"message", "function_call"}, types)
}

// Content that arrives after [DONE] must not emit any event after the
// terminal one: response.completed already declared the full output.
func TestOpenAIResponsesStreamConverter_LateContentAfterDoneEmitsNothing(t *testing.T) {
	stream := `data: {"choices":[{"index":0,"delta":{"content":"A"},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1n"}}]},"finish_reason":"stop"}]}

data: [DONE]

data: {"choices":[{"index":0,"delta":{"content":"late"},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,bGF0ZQ=="}}]},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"content":"late","tool_calls":"off-spec"},"finish_reason":null}]}
`
	converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(stream)), "test-model", "gemini")
	raw, err := io.ReadAll(converter)
	require.NoError(t, err)

	events := parseTestSSEEvents(t, string(raw))
	require.GreaterOrEqual(t, len(events), 2)
	assert.Equal(t, "response.completed", events[len(events)-2].Name)
	assert.True(t, events[len(events)-1].Done, "only [DONE] follows response.completed")
}

// A signature-only delta after an image must not change the message item the
// image closed, whose output_item.done is already out. It goes on the next
// message item, or on a trailing empty one when no text follows, mirroring
// Gemini's trailing empty part that carries the signature.
func TestOpenAIResponsesStreamConverter_SignatureOnlyDeltaAfterImage(t *testing.T) {
	const (
		text      = `{"choices":[{"index":0,"delta":{"content":"A"},"finish_reason":null}]}`
		image     = `{"choices":[{"index":0,"delta":{"images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1n"}}]},"finish_reason":null}]}`
		signature = `{"choices":[{"index":0,"delta":{"content":"","extra_content":{"google":{"thought_signature":"sig"}}},"finish_reason":null}]}`
		tolerant  = `{"choices":[{"index":0,"delta":{"tool_calls":"off-spec","extra_content":{"google":{"thought_signature":"sig"}}},"finish_reason":null}]}`
		textB     = `{"choices":[{"index":0,"delta":{"content":"B"},"finish_reason":null}]}`
		stop      = `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
	)
	tests := []struct {
		name     string
		chunks   []string
		wantLast string
	}{
		{name: "text follows", chunks: []string{text, image, signature, textB, stop}, wantLast: "B"},
		{name: "no text follows", chunks: []string{text, image, signature, stop}, wantLast: ""},
		{name: "no text follows on tolerant path", chunks: []string{text, image, tolerant, stop}, wantLast: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream := "data: " + strings.Join(tt.chunks, "\n\ndata: ") + "\n\ndata: [DONE]\n\n"
			_, done, completed := convertTestStream(t, stream)

			output := outputItems(t, completed)
			require.Len(t, output, 3)
			assert.Equal(t, []any{"message", "image_generation_call", "message"}, []any{output[0]["type"], output[1]["type"], output[2]["type"]})
			assert.Nil(t, output[0]["extra_content"], "the closed message keeps what its output_item.done declared")
			assert.Equal(t, map[string]any{"google": map[string]any{"thought_signature": "sig"}}, output[2]["extra_content"])
			content, ok := output[2]["content"].([]any)
			require.True(t, ok)
			require.Len(t, content, 1)
			part, ok := content[0].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, tt.wantLast, part["text"])
			for _, item := range done {
				for _, final := range output {
					if item["id"] == final["id"] {
						assert.Equal(t, final["extra_content"], item["extra_content"], "terminal item matches its output_item.done")
					}
				}
			}
		})
	}
}

package anthropic

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const midStreamErrorSSE = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-5","usage":{"input_tokens":3,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}

event: error
data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}

`

func TestStreamConverter_ErrorEventFailsStream(t *testing.T) {
	converter := newStreamConverter(io.NopCloser(strings.NewReader(midStreamErrorSSE)), "claude-sonnet-4-5", "anthropic")

	raw, err := io.ReadAll(converter)
	require.NoError(t, err)

	out := string(raw)
	assert.Contains(t, out, `"content":"Hel"`)
	assert.Contains(t, out, `"error":{`)
	assert.Contains(t, out, `"message":"Overloaded"`)
	assert.Contains(t, out, `"code":"overloaded_error"`)
	assert.NotContains(t, out, "[DONE]")
	require.NoError(t, converter.Close())
}

func TestResponsesStreamConverter_ErrorEventFailsResponse(t *testing.T) {
	converter := newResponsesStreamConverter(io.NopCloser(strings.NewReader(midStreamErrorSSE)), "claude-sonnet-4-5", "anthropic")

	raw, err := io.ReadAll(converter)
	require.NoError(t, err)

	out := string(raw)
	assert.Contains(t, out, `"delta":"Hel"`)
	assert.Contains(t, out, "event: response.failed")
	assert.Contains(t, out, `"message":"Overloaded"`)
	assert.Contains(t, out, `"code":"overloaded_error"`)
	assert.NotContains(t, out, "response.completed")
	assert.NotContains(t, out, "response.incomplete")

	_, data, found := strings.Cut(out, "event: response.failed\ndata: ")
	require.True(t, found)
	data, _, _ = strings.Cut(data, "\n")
	var failed struct {
		Response struct {
			Output []struct {
				Status  string `json:"status"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"output"`
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		} `json:"response"`
	}
	require.NoError(t, json.Unmarshal([]byte(data), &failed))
	require.Len(t, failed.Response.Output, 1)
	assert.Equal(t, "incomplete", failed.Response.Output[0].Status)
	require.Len(t, failed.Response.Output[0].Content, 1)
	assert.Equal(t, "Hel", failed.Response.Output[0].Content[0].Text)
	assert.Equal(t, 3, failed.Response.Usage.InputTokens)
	assert.Equal(t, 1, failed.Response.Usage.OutputTokens)
	require.NoError(t, converter.Close())
}

package chatgpt

import (
	"bufio"
	"bytes"
	"io"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// maxSSELineBytes caps a single SSE data line. Reasoning summaries and
// encrypted reasoning blobs are large, so the default bufio limit is too small.
const maxSSELineBytes = 8 << 20

// collapseResponsesStream reads a Responses SSE stream and returns the response
// object carried by its terminal event. The Codex backend streams only, so this
// is how GoModel answers a non-streaming /v1/responses call against it.
func collapseResponsesStream(stream io.Reader) (*core.ResponsesResponse, error) {
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, 64<<10), maxSSELineBytes)

	var final *core.ResponsesResponse
	for scanner.Scan() {
		data, ok := bytes.CutPrefix(bytes.TrimSpace(scanner.Bytes()), []byte("data:"))
		if !ok {
			continue
		}
		data = bytes.TrimSpace(data)
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		var event struct {
			Type     string                  `json:"type"`
			Response *core.ResponsesResponse `json:"response"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			continue
		}
		// Keep the last response envelope seen: completed and failed both carry
		// the full object, and an incomplete stream should still report what
		// the upstream last knew.
		if event.Response != nil {
			final = event.Response
		}
		if event.Type == "response.completed" || event.Type == "response.failed" {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, core.NewProviderError("chatgpt", 502, "failed to read response stream: "+err.Error(), err)
	}
	if final == nil {
		return nil, core.NewEmptyProviderResponseError("chatgpt")
	}
	return final, nil
}

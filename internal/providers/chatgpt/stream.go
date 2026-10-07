package chatgpt

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"sort"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// maxSSELineBytes caps a single SSE data line. Reasoning summaries and
// encrypted reasoning blobs are large, so the default bufio limit is too small.
const maxSSELineBytes = 8 << 20

// collapseResponsesStream reads a Responses SSE stream and returns the response
// object carried by its terminal event. The Codex backend streams only, so this
// is how GoModel answers a non-streaming /v1/responses call against it.
//
// The backend's terminal event carries an empty output list; the items arrive
// only as response.output_item.done events, so those fill it in.
//
// Only a terminal lifecycle event produces a response. A stream that stops
// early — a dropped connection, or an `error` event — is an error rather than
// the last in-progress envelope, which would otherwise be served as an empty
// but successful answer.
func collapseResponsesStream(stream io.Reader) (*core.ResponsesResponse, error) {
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, 64<<10), maxSSELineBytes)

	var items []indexedOutputItem
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
			Type        string                    `json:"type"`
			Message     string                    `json:"message"`
			Response    *core.ResponsesResponse   `json:"response"`
			OutputIndex int                       `json:"output_index"`
			Item        *core.ResponsesOutputItem `json:"item"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			continue
		}
		switch event.Type {
		case "response.output_item.done":
			if event.Item != nil {
				items = append(items, indexedOutputItem{index: event.OutputIndex, item: *event.Item})
			}
		// The three terminal lifecycle events all carry the full object.
		// failed and incomplete are reported to the caller as a normal
		// response whose status says so, matching what the Responses API
		// returns for a non-streaming call.
		case "response.completed", "response.failed", "response.incomplete":
			if event.Response == nil {
				return nil, core.NewEmptyProviderResponseError("chatgpt")
			}
			if len(event.Response.Output) == 0 {
				event.Response.Output = streamedOutput(items)
			}
			return event.Response, nil
		case "error":
			message := event.Message
			if message == "" {
				message = "upstream reported a stream error"
			}
			return nil, core.NewProviderError("chatgpt", http.StatusBadGateway, message, nil)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, core.NewProviderError("chatgpt", http.StatusBadGateway,
			"failed to read response stream: "+err.Error(), err)
	}
	return nil, core.NewProviderError("chatgpt", http.StatusBadGateway,
		"response stream ended before completion", nil)
}

// indexedOutputItem is one finished output item and its position in the
// response's output list.
type indexedOutputItem struct {
	index int
	item  core.ResponsesOutputItem
}

// streamedOutput orders the streamed output items by their output_index. It
// returns nil when the stream carried none.
func streamedOutput(items []indexedOutputItem) []core.ResponsesOutputItem {
	if len(items) == 0 {
		return nil
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].index < items[j].index })
	output := make([]core.ResponsesOutputItem, len(items))
	for i, entry := range items {
		output[i] = entry.item
	}
	return output
}

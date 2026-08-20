package chatgpt

import (
	"bufio"
	"bytes"
	"io"
	"net/http"

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
// Only a terminal lifecycle event produces a response. A stream that stops
// early — a dropped connection, or an `error` event — is an error rather than
// the last in-progress envelope, which would otherwise be served as an empty
// but successful answer.
func collapseResponsesStream(stream io.Reader) (*core.ResponsesResponse, error) {
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, 64<<10), maxSSELineBytes)

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
			Message  string                  `json:"message"`
			Response *core.ResponsesResponse `json:"response"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			continue
		}
		switch event.Type {
		// The three terminal lifecycle events all carry the full object.
		// failed and incomplete are reported to the caller as a normal
		// response whose status says so, matching what the Responses API
		// returns for a non-streaming call.
		case "response.completed", "response.failed", "response.incomplete":
			if event.Response == nil {
				return nil, core.NewEmptyProviderResponseError("chatgpt")
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

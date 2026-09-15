package server

import (
	"bytes"
	"errors"
	"io"
	"strings"

	"github.com/goccy/go-json"
	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/core"
)

// ErrStreamIncomplete reports a provider stream that ended before its
// terminal event.
var ErrStreamIncomplete = errors.New("provider stream ended before completion")

// maxGuardLineBytes caps how much of one SSE line the completion guard keeps
// to classify it. Terminal events are small; a longer line is content.
const maxGuardLineBytes = 64 * 1024

type streamKind int

const (
	chatStream streamKind = iota + 1
	responsesStream
)

// streamKindForPath names the SSE dialect a route streams to the client, or
// 0 for routes the guard leaves alone. /v1/messages is converted from a chat
// stream after this point, so its converter reports truncation itself.
func streamKindForPath(path string) streamKind {
	switch {
	case strings.HasSuffix(path, "/chat/completions"):
		return chatStream
	case strings.HasSuffix(path, "/responses"):
		return responsesStream
	}
	return 0
}

// guardStreamCompletion makes a stream that stops before its terminal event
// end with an explicit error event in the route's dialect, instead of a
// silent EOF the client cannot tell from a complete answer. The upstream
// bytes pass through unchanged. After the error event, Read returns the
// read failure, or ErrStreamIncomplete for a clean EOF, so the truncation is
// recorded as a stream error.
func guardStreamCompletion(path string, stream io.ReadCloser) io.ReadCloser {
	kind := streamKindForPath(path)
	if kind == 0 || stream == nil {
		return stream
	}
	return &completionGuard{ReadCloser: stream, kind: kind}
}

type completionGuard struct {
	io.ReadCloser
	kind      streamKind
	line      []byte
	longLine  bool
	completed bool
	tail      []byte
	endErr    error
}

func (g *completionGuard) Read(p []byte) (int, error) {
	if g.endErr != nil {
		if len(g.tail) > 0 {
			n := copy(p, g.tail)
			g.tail = g.tail[n:]
			return n, nil
		}
		return 0, g.endErr
	}

	n, err := g.ReadCloser.Read(p)
	if n > 0 && !g.completed {
		g.observe(p[:n])
	}
	if err == nil || g.completed {
		return n, err
	}
	// A final line without its newline still counts.
	g.classifyLine()
	if g.completed {
		return n, err
	}
	g.endErr = err
	if err == io.EOF {
		g.endErr = ErrStreamIncomplete
	}
	g.tail = streamErrorEvent(g.kind)
	return n, nil
}

func (g *completionGuard) observe(chunk []byte) {
	for len(chunk) > 0 && !g.completed {
		idx := bytes.IndexByte(chunk, '\n')
		if idx == -1 {
			g.appendLine(chunk)
			return
		}
		g.appendLine(chunk[:idx])
		g.classifyLine()
		chunk = chunk[idx+1:]
	}
}

func (g *completionGuard) appendLine(part []byte) {
	if g.longLine {
		return
	}
	if len(g.line)+len(part) > maxGuardLineBytes {
		g.longLine = true
		g.line = g.line[:0]
		return
	}
	g.line = append(g.line, part...)
}

func (g *completionGuard) classifyLine() {
	line := bytes.TrimSpace(g.line)
	long := g.longLine
	g.line = g.line[:0]
	g.longLine = false
	if long {
		return
	}
	payload, ok := bytes.CutPrefix(line, []byte("data:"))
	if !ok {
		return
	}
	g.completed = isTerminalPayload(g.kind, bytes.TrimSpace(payload))
}

// isTerminalPayload reports whether one SSE data payload ends the stream: the
// [DONE] marker, an in-band error (already relayed to the client), or the
// dialect's own terminal event.
func isTerminalPayload(kind streamKind, payload []byte) bool {
	if bytes.Equal(payload, []byte("[DONE]")) {
		return true
	}
	if len(payload) == 0 || payload[0] != '{' || !gjson.ValidBytes(payload) {
		return false
	}
	if errMember := gjson.GetBytes(payload, "error"); errMember.Exists() && errMember.Type != gjson.Null {
		return true
	}
	switch kind {
	case chatStream:
		for _, reason := range gjson.GetBytes(payload, "choices.#.finish_reason").Array() {
			if reason.String() != "" {
				return true
			}
		}
	case responsesStream:
		switch gjson.GetBytes(payload, "type").String() {
		case "response.completed", "response.incomplete", "response.failed", "response.done", "error":
			return true
		}
	}
	return false
}

// streamErrorEvent is the dialect's error event for a truncated stream. The
// message stays generic: the underlying read error can name upstream hosts.
func streamErrorEvent(kind streamKind) []byte {
	message := ErrStreamIncomplete.Error()
	var out bytes.Buffer
	out.WriteString("\n")
	var payload any
	if kind == responsesStream {
		out.WriteString("event: error\n")
		payload = map[string]any{"type": "error", "code": "stream_incomplete", "message": message, "param": nil}
	} else {
		payload = map[string]any{"error": map[string]any{
			"type": string(core.ErrorTypeProvider), "message": message, "param": nil, "code": "stream_incomplete",
		}}
	}
	body, _ := json.Marshal(payload)
	out.WriteString("data: ")
	out.Write(body)
	out.WriteString("\n\n")
	return out.Bytes()
}

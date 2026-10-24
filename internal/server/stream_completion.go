package server

import (
	"bytes"
	"io"
	"regexp"
	"strings"

	"github.com/goccy/go-json"
	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/streaming"
)

const (
	// maxGuardLineBytes caps how much of one SSE line the completion guard
	// parses as JSON.
	maxGuardLineBytes = 64 * 1024
	// guardEdgeBytes is how much of each end of a longer line is kept to
	// classify it: a large final chat chunk or a response.completed event
	// that carries the whole response.
	guardEdgeBytes = 4 * 1024
)

// Terminal markers of a payload too long to parse. Quotes escaped inside
// string content never match them.
var (
	longInBandError       = regexp.MustCompile(`^\{\s*"error"\s*:\s*\{`)
	longChatTerminal      = regexp.MustCompile(`"finish_reason"\s*:\s*"`)
	longResponsesTerminal = regexp.MustCompile(`"type"\s*:\s*"(?:response\.(?:completed|incomplete|failed|done)|error)"`)
)

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
// bytes pass through unchanged. After the error event, Read returns the read
// failure wrapped in streaming.ErrStreamIncomplete, so the truncation is
// recorded as a provider stream error.
func guardStreamCompletion(path string, stream io.ReadCloser) io.ReadCloser {
	kind := streamKindForPath(path)
	if kind == 0 || stream == nil {
		return stream
	}
	return &completionGuard{ReadCloser: stream, kind: kind}
}

type completionGuard struct {
	io.ReadCloser
	kind streamKind
	// line holds the current line, or only its first guardEdgeBytes once
	// longLine is set; tail then holds its last guardEdgeBytes.
	line      []byte
	tail      []byte
	longLine  bool
	completed bool
	errEvent  []byte
	endErr    error
}

func (g *completionGuard) Read(p []byte) (int, error) {
	if g.endErr != nil {
		if len(g.errEvent) > 0 {
			n := copy(p, g.errEvent)
			g.errEvent = g.errEvent[n:]
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
	g.endErr = streaming.IncompleteStreamError(err)
	g.errEvent = streamErrorEvent(g.kind)
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
		g.keepTail(part)
		return
	}
	if len(g.line)+len(part) <= maxGuardLineBytes {
		g.line = append(g.line, part...)
		return
	}
	// Too long to parse: keep only its first and last guardEdgeBytes.
	g.longLine = true
	g.tail = append(g.tail[:0], g.line...)
	g.keepTail(part)
	if len(g.line) < guardEdgeBytes {
		g.line = append(g.line, part[:min(len(part), guardEdgeBytes-len(g.line))]...)
	}
	g.line = g.line[:min(len(g.line), guardEdgeBytes)]
}

func (g *completionGuard) keepTail(part []byte) {
	g.tail = append(g.tail, part...)
	if extra := len(g.tail) - guardEdgeBytes; extra > 0 {
		g.tail = append(g.tail[:0], g.tail[extra:]...)
	}
}

func (g *completionGuard) classifyLine() {
	head, tail, long := g.line, g.tail, g.longLine
	g.line, g.tail, g.longLine = g.line[:0], g.tail[:0], false
	payload, ok := bytes.CutPrefix(bytes.TrimSpace(head), []byte("data:"))
	if !ok {
		return
	}
	payload = bytes.TrimSpace(payload)
	if long {
		g.completed = isTerminalLongPayload(g.kind, payload, tail)
		return
	}
	g.completed = isTerminalPayload(g.kind, payload)
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

// isTerminalLongPayload classifies a payload too long to parse from its
// first bytes (head) and last bytes (tail): an in-band error opens with its
// error member, a Responses event names its type before the response it
// carries, and a chat chunk's finish_reason follows its long delta.
func isTerminalLongPayload(kind streamKind, head, tail []byte) bool {
	if longInBandError.Match(head) {
		return true
	}
	switch kind {
	case chatStream:
		return longChatTerminal.Match(tail)
	case responsesStream:
		return longResponsesTerminal.Match(head)
	}
	return false
}

// streamErrorEvent is the dialect's error event for a truncated stream. The
// message stays generic: the underlying read error can name upstream hosts.
func streamErrorEvent(kind streamKind) []byte {
	message := streaming.ErrStreamIncomplete.Error()
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

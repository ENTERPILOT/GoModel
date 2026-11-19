package m365copilot

import (
	"bufio"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

// maxStreamEventBytes bounds one line of the upstream stream. Every event
// repeats the whole answer so far together with its citations and cards.
const maxStreamEventBytes = 8 * 1024 * 1024

type chatStream struct {
	reader *io.PipeReader
	body   io.ReadCloser
}

func newChatStream(body io.ReadCloser, state *streamState) io.ReadCloser {
	reader, writer := io.Pipe()
	go convertChatStream(body, writer, state)
	return &chatStream{reader: reader, body: body}
}

func (s *chatStream) Read(p []byte) (int, error) { return s.reader.Read(p) }

func (s *chatStream) Close() error {
	_ = s.reader.Close()
	return s.body.Close()
}

// convertChatStream turns the chatOverStream events into chat completion
// chunks. Each event is a snapshot of the conversation, so only the text added
// since the previous snapshot is forwarded.
//
// The reference prints each event's JSON indented, with "data:" on its first
// line only. Lines that are neither blank nor an SSE field are therefore read
// as a continuation of the event's data, so the stream parses whether the
// service sends the JSON on one line or as printed.
// https://learn.microsoft.com/microsoft-365-copilot/extensibility/api/ai-services/chat/copilotconversation-chatoverstream
func convertChatStream(body io.ReadCloser, out *io.PipeWriter, state *streamState) {
	defer func() { _ = body.Close() }()
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxStreamEventBytes)
	var data strings.Builder
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		switch {
		case line == "":
			if err := state.consume(out, data.String()); err != nil {
				state.fail(out, err)
				return
			}
			data.Reset()
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(line, "data:"))
		case isSSEField(line):
		case data.Len() > 0:
			data.WriteByte('\n')
			data.WriteString(line)
		}
	}
	if err := state.consume(out, data.String()); err != nil {
		state.fail(out, err)
		return
	}
	if err := scanner.Err(); err != nil {
		state.fail(out, core.NewProviderError(state.provider, http.StatusBadGateway, "failed to read the Microsoft 365 Copilot stream", err))
		return
	}
	state.finish(out)
}

// isSSEField reports whether line is a comment or a field other than data.
func isSSEField(line string) bool {
	for _, prefix := range []string{":", "id:", "event:", "retry:"} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

type streamState struct {
	id           string
	model        string
	provider     string
	prompt       string
	includeUsage bool
	created      int64
	// sent is the answer text forwarded so far.
	sent string
	// disengaged records that Copilot's content checks ended the conversation.
	disengaged bool
}

// streamEvent is one event: a conversation snapshot, or an error object.
type streamEvent struct {
	conversation
	Error json.RawMessage `json:"error"`
}

// consume forwards the text one event adds to the answer. A snapshot that
// rewrites text already forwarded cannot be expressed as a delta and is
// skipped; forwarding resumes with the next snapshot that extends it.
func (s *streamState) consume(out io.Writer, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[DONE]" {
		return nil
	}
	var event streamEvent
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		return core.NewProviderError(s.provider, http.StatusBadGateway, "failed to parse a Microsoft 365 Copilot stream event", err)
	}
	if len(event.Error) > 0 && !core.IsJSONNull(event.Error) {
		return core.ParseProviderError(s.provider, http.StatusBadGateway, []byte(raw), nil)
	}
	if event.State == stateDisengagedForRAI {
		s.disengaged = true
	}
	text, ok := event.answer(s.prompt)
	if !ok || len(text) <= len(s.sent) || !strings.HasPrefix(text, s.sent) {
		return nil
	}
	delta := map[string]any{"content": text[len(s.sent):]}
	if s.sent == "" {
		delta["role"] = "assistant"
	}
	s.sent = text
	return s.write(out, delta, nil, nil)
}

// finish ends the stream. A stream that carried no answer is an upstream
// failure unless the conversation was ended by Copilot's content checks.
func (s *streamState) finish(out *io.PipeWriter) {
	if s.sent == "" && !s.disengaged {
		s.fail(out, core.NewProviderError(s.provider, http.StatusBadGateway, "the Microsoft 365 Copilot stream ended without an answer", nil))
		return
	}
	var usage map[string]any
	if s.includeUsage {
		// Copilot reports no token counts.
		usage = map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}
	}
	if err := s.write(out, map[string]any{}, finishReason(s.disengaged), usage); err != nil {
		_ = out.CloseWithError(err)
		return
	}
	s.done(out)
}

// fail reports err as an OpenAI-compatible error chunk and ends the stream
// normally, so the Responses converter can emit its terminal failure event.
func (s *streamState) fail(out *io.PipeWriter, err error) {
	message := err.Error()
	var gatewayErr *core.GatewayError
	if errors.As(err, &gatewayErr) && strings.TrimSpace(gatewayErr.Message) != "" {
		message = gatewayErr.Message
	}
	chunk, marshalErr := json.Marshal(map[string]any{"error": map[string]any{
		"type":    core.ErrorTypeProvider,
		"message": message,
		"param":   nil,
		"code":    nil,
	}})
	if marshalErr != nil {
		_ = out.CloseWithError(marshalErr)
		return
	}
	if _, writeErr := io.WriteString(out, "data: "+string(chunk)+"\n\n"); writeErr != nil {
		_ = out.CloseWithError(writeErr)
		return
	}
	s.done(out)
}

func (s *streamState) done(out *io.PipeWriter) {
	if _, err := io.WriteString(out, "data: [DONE]\n\n"); err != nil {
		_ = out.CloseWithError(err)
		return
	}
	_ = out.Close()
}

func (s *streamState) write(out io.Writer, delta map[string]any, finish any, usage map[string]any) error {
	_, err := io.WriteString(out, providers.FormatChatChunkSSE(s.id, s.created, s.model, s.provider, delta, finish, usage))
	return err
}

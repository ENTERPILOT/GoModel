package server

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/goccy/go-json"
)

// persistingStream commits the turn before releasing the provider's terminal
// event to the client. A storage failure therefore interrupts the SSE stream
// instead of reporting response.completed with history that was not saved.
func (t *conversationTurn) persistingStream(ctx context.Context, stream io.ReadCloser) io.ReadCloser {
	return &conversationPersistingStream{
		upstream: stream,
		reader:   bufio.NewReader(stream),
		observer: &conversationStreamObserver{turn: t, ctx: context.WithoutCancel(ctx)},
	}
}

type conversationStreamObserver struct {
	turn      *conversationTurn
	ctx       context.Context
	attempted bool
	err       error
}

func (o *conversationStreamObserver) OnJSONEvent(payload map[string]any) {
	eventType, _ := payload["type"].(string)
	if o.attempted || (eventType != "response.completed" && eventType != "response.done") {
		return
	}
	response, ok := payload["response"].(map[string]any)
	if !ok {
		return
	}
	o.attempted = true
	responseID, _ := response["id"].(string)
	outputItems, _ := response["output"].([]any)
	output := make([]json.RawMessage, 0, len(outputItems))
	for _, item := range outputItems {
		raw, err := json.Marshal(item)
		if err != nil {
			o.err = fmt.Errorf("marshal streamed conversation output: %w", err)
			return
		}
		output = append(output, raw)
	}
	if _, err := o.turn.appendExchange(o.ctx, responseID, output); err != nil {
		o.err = fmt.Errorf("append streamed conversation turn: %w", err)
	}
}

func (o *conversationStreamObserver) OnStreamClose() {
	if o.err != nil {
		slog.Warn("conversation stream persistence failed", "conversation_id", o.turn.id, "error", o.err)
	}
}

// conversationPersistingStream buffers one complete SSE event at a time. This
// keeps ordinary event-level streaming while ensuring no prefix of a terminal
// success event escapes before its conversation turn has been committed.
type conversationPersistingStream struct {
	upstream io.ReadCloser
	reader   *bufio.Reader
	observer *conversationStreamObserver
	ready    []byte
	readErr  error
	closed   bool
}

func (s *conversationPersistingStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if s.observer.err != nil {
		return 0, s.observer.err
	}
	for len(s.ready) == 0 {
		if s.readErr != nil {
			return 0, s.readErr
		}

		event, err := s.readEvent()
		if len(event) == 0 {
			return 0, err
		}
		s.observeEvent(event)
		if s.observer.err != nil {
			return 0, s.observer.err
		}
		s.ready = event
		s.readErr = err
	}

	n := copy(p, s.ready)
	s.ready = s.ready[n:]
	return n, nil
}

func (s *conversationPersistingStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	s.observer.OnStreamClose()
	return s.upstream.Close()
}

func (s *conversationPersistingStream) readEvent() ([]byte, error) {
	var event []byte
	for {
		line, err := s.reader.ReadBytes('\n')
		event = append(event, line...)
		if bytes.Equal(line, []byte("\n")) || bytes.Equal(line, []byte("\r\n")) {
			return event, err
		}
		if err != nil {
			return event, err
		}
	}
}

func (s *conversationPersistingStream) observeEvent(event []byte) {
	payload, ok := conversationSSEPayload(event)
	if ok {
		s.observer.OnJSONEvent(payload)
	}
}

func conversationSSEPayload(event []byte) (map[string]any, bool) {
	lines := bytes.Split(event, []byte("\n"))
	dataLines := make([][]byte, 0, len(lines))
	for _, line := range lines {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := line[len("data:"):]
		if len(data) > 0 && data[0] == ' ' {
			data = data[1:]
		}
		dataLines = append(dataLines, data)
	}
	if len(dataLines) == 0 {
		return nil, false
	}
	data := bytes.Join(dataLines, []byte("\n"))
	if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
		return nil, false
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, false
	}
	return payload, true
}

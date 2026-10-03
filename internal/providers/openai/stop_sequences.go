package openai

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/streaming"
)

// OpenAI reasoning models (GPT-5 and later, o-series) reject the stop
// parameter. The provider emulates it instead: the sequences are taken off the
// request and the answer is cut at the first one, reported as finish_reason
// "stop" with the matched sequence in the stop_sequence extension (which the
// Anthropic Messages API renders as stop_reason "stop_sequence").

// takeStopSequences removes stop from a request to a model that rejects it and
// returns the sequences to emulate. Other requests are returned unchanged.
func takeStopSequences(req *core.ChatRequest) (*core.ChatRequest, []string, error) {
	if req == nil || !isReasoningChatModel(req.Model) {
		return req, nil, nil
	}
	raw := bytes.TrimSpace(req.ExtraFields.Lookup("stop"))
	if len(raw) == 0 {
		return req, nil, nil
	}
	var stops []string
	if !core.IsJSONNull(raw) {
		if raw[0] == '"' {
			var stop string
			if err := json.Unmarshal(raw, &stop); err != nil {
				return nil, nil, invalidStopError(err)
			}
			stops = []string{stop}
		} else if err := json.Unmarshal(raw, &stops); err != nil {
			return nil, nil, invalidStopError(err)
		}
	}
	adapted := *req
	adapted.ExtraFields = req.ExtraFields.Without("stop")
	nonEmpty := stops[:0]
	for _, stop := range stops {
		if stop != "" {
			nonEmpty = append(nonEmpty, stop)
		}
	}
	return &adapted, nonEmpty, nil
}

func invalidStopError(err error) error {
	return core.NewInvalidRequestError("stop must be a string or an array of strings", err).WithParam("stop")
}

// firstStop returns the byte offset of the earliest stop sequence in text and
// the sequence, or -1 when none occurs.
func firstStop(text string, stops []string) (int, string) {
	at, matched := -1, ""
	for _, stop := range stops {
		if i := strings.Index(text, stop); i >= 0 && (at < 0 || i < at) {
			at, matched = i, stop
		}
	}
	return at, matched
}

// applyStopSequences cuts each choice's text at its first stop sequence. A
// choice that also called tools keeps its tool calls and finish reason, so the
// caller can continue its tool loop.
func applyStopSequences(resp *core.ChatResponse, stops []string) {
	if resp == nil || len(stops) == 0 {
		return
	}
	for i := range resp.Choices {
		choice := &resp.Choices[i]
		text, ok := choice.Message.Content.(string)
		if !ok {
			continue
		}
		at, stop := firstStop(text, stops)
		if at < 0 {
			continue
		}
		choice.Message.Content = text[:at]
		if len(choice.Message.ToolCalls) == 0 {
			choice.FinishReason = "stop"
			choice.StopSequence = stop
		}
	}
}

// stopSequenceStream emulates stop sequences on a chat completion SSE stream.
// It holds back the text a stop sequence could still complete and cuts the
// text at the first match, dropping what follows. Tool calls and the upstream
// finish still pass, and the stream is read to its end so the usage chunk
// arrives; a choice that called no tools finishes as stopped by the sequence.
// It is single-reader.
type stopSequenceStream struct {
	reader   *bufio.Reader
	body     io.ReadCloser
	buffer   streaming.StreamBuffer
	stops    []string
	holdBack int
	choices  map[float64]*stopChoiceState
	err      error
}

type stopChoiceState struct {
	pending      string
	stop         string // the matched stop sequence, once the text is cut
	sawToolCalls bool
}

func newStopSequenceStream(body io.ReadCloser, stops []string) io.ReadCloser {
	holdBack := 0
	for _, stop := range stops {
		holdBack = max(holdBack, len(stop)-1)
	}
	return &stopSequenceStream{
		reader:   bufio.NewReader(body),
		body:     body,
		buffer:   streaming.NewStreamBuffer(1024),
		stops:    stops,
		holdBack: holdBack,
		choices:  make(map[float64]*stopChoiceState),
	}
}

// Read returns the rewritten stream. An upstream error (or io.EOF) is
// returned once the data read before it has been delivered.
func (s *stopSequenceStream) Read(p []byte) (int, error) {
	for {
		if s.buffer.Len() > 0 {
			return s.buffer.Read(p), nil
		}
		if s.err != nil {
			return 0, s.err
		}
		line, err := s.reader.ReadBytes('\n')
		if len(line) > 0 {
			s.handleLine(line)
		}
		if err != nil {
			s.err = err
		}
	}
}

// Close releases the buffer and closes the upstream body.
func (s *stopSequenceStream) Close() error {
	s.buffer.Release()
	return s.body.Close()
}

// handleLine rewrites one SSE line; anything but a chunk passes through.
func (s *stopSequenceStream) handleLine(line []byte) {
	payload, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:"))
	var chunk map[string]any
	if !ok || json.Unmarshal(bytes.TrimSpace(payload), &chunk) != nil {
		s.buffer.AppendBytes(line)
		return
	}
	choices, _ := chunk["choices"].([]any)
	for _, raw := range choices {
		if choice, ok := raw.(map[string]any); ok {
			s.rewriteChoice(choice)
		}
	}
	rewritten, err := json.Marshal(chunk)
	if err != nil {
		s.buffer.AppendBytes(line)
		return
	}
	s.buffer.AppendString("data: " + string(rewritten) + "\n\n")
}

// rewriteChoice applies the stop sequences to one streamed choice.
func (s *stopSequenceStream) rewriteChoice(choice map[string]any) {
	index, _ := choice["index"].(float64)
	state := s.choices[index]
	if state == nil {
		state = &stopChoiceState{}
		s.choices[index] = state
	}
	delta, _ := choice["delta"].(map[string]any)
	if delta == nil {
		delta = map[string]any{}
		choice["delta"] = delta
	}
	if delta["tool_calls"] != nil {
		state.sawToolCalls = true
	}
	content, hasContent := delta["content"].(string)
	switch {
	case state.stop != "":
		delete(delta, "content") // the text ended at the stop sequence
	case hasContent || state.pending != "":
		text := state.pending + content
		if at, stop := firstStop(text, s.stops); at >= 0 {
			delta["content"], state.pending, state.stop = text[:at], "", stop
			break
		}
		emit := len(text) - s.holdBack
		if choice["finish_reason"] != nil || delta["tool_calls"] != nil {
			emit = len(text) // nothing can follow to complete a stop sequence
		}
		emit = max(emit, 0)
		for emit > 0 && emit < len(text) && !utf8.RuneStart(text[emit]) {
			emit--
		}
		delta["content"], state.pending = text[:emit], text[emit:]
	}
	if choice["finish_reason"] != nil && state.stop != "" && !state.sawToolCalls {
		choice["finish_reason"] = "stop"
		delta["stop_sequence"] = state.stop
	}
}

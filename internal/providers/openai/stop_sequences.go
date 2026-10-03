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

// applyStopSequences cuts each choice's text at its first stop sequence.
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
		if at, stop := firstStop(text, stops); at >= 0 {
			choice.Message.Content = text[:at]
			choice.Message.ToolCalls = nil
			choice.FinishReason = "stop"
			choice.StopSequence = stop
		}
	}
}

// stopSequenceStream emulates stop sequences on a chat completion SSE stream.
// It holds back the text a stop sequence could still complete, cuts the answer
// at the first match and finishes that choice there, then keeps reading the
// upstream so its usage chunk still arrives. It is single-reader.
type stopSequenceStream struct {
	reader   *bufio.Reader
	body     io.ReadCloser
	buffer   streaming.StreamBuffer
	stops    []string
	holdBack int
	choices  map[float64]*stopChoiceState
	done     bool
}

type stopChoiceState struct {
	pending string
	stopped bool
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

// Read returns the rewritten stream, passing upstream errors through.
func (s *stopSequenceStream) Read(p []byte) (int, error) {
	for {
		if s.buffer.Len() > 0 {
			return s.buffer.Read(p), nil
		}
		if s.done {
			return 0, io.EOF
		}
		line, err := s.reader.ReadBytes('\n')
		if len(line) > 0 {
			s.handleLine(line)
		}
		if err != nil {
			if s.buffer.Len() > 0 {
				s.done = true
				return s.buffer.Read(p), nil
			}
			return 0, err
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
	kept := make([]any, 0, len(choices))
	for _, raw := range choices {
		if choice, ok := raw.(map[string]any); ok && s.rewriteChoice(choice) {
			kept = append(kept, choice)
		}
	}
	if len(kept) == 0 && len(choices) > 0 && chunk["usage"] == nil {
		return
	}
	chunk["choices"] = kept
	rewritten, err := json.Marshal(chunk)
	if err != nil {
		s.buffer.AppendBytes(line)
		return
	}
	s.buffer.AppendString("data: " + string(rewritten) + "\n\n")
}

// rewriteChoice applies the stop sequences to one streamed choice and reports
// whether it is still sent; a choice already finished at a stop sequence is
// dropped.
func (s *stopSequenceStream) rewriteChoice(choice map[string]any) bool {
	index, _ := choice["index"].(float64)
	state := s.choices[index]
	if state == nil {
		state = &stopChoiceState{}
		s.choices[index] = state
	}
	if state.stopped {
		return false
	}
	delta, _ := choice["delta"].(map[string]any)
	if delta == nil {
		delta = map[string]any{}
		choice["delta"] = delta
	}
	content, hasContent := delta["content"].(string)
	if hasContent || state.pending != "" {
		text := state.pending + content
		if at, stop := firstStop(text, s.stops); at >= 0 {
			delta["content"] = text[:at]
			delete(delta, "tool_calls")
			delta["stop_sequence"] = stop
			choice["finish_reason"] = "stop"
			state.pending, state.stopped = "", true
			return true
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
	return true
}

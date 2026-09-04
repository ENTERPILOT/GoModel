package streaming

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/goccy/go-json"
)

// DefaultFinishReason is the chat finish_reason used when a stream is cut
// without an explicit one.
const DefaultFinishReason = "content_filter"

// Termination describes how a cut stream ends.
type Termination struct {
	// FinishReason is the chat finish_reason (default "content_filter"). For
	// Responses it selects incomplete_details.reason.
	FinishReason string
	// ErrorCode, when set, ends the stream with an error (chat: an error
	// payload before the finish chunk; Responses: response.failed).
	ErrorCode    string
	ErrorMessage string
	// Text is optional final text emitted as one more delta before finishing,
	// for example a canned safe message.
	Text string
	// Usage, when set, is the provider usage object rendered into the
	// terminal event (chat: the finish chunk; Responses: response.usage), so
	// accounting observers downstream still see the tokens a cut stream
	// consumed.
	Usage any
}

func (t Termination) finishReason() string {
	if t.FinishReason == "" {
		return DefaultFinishReason
	}
	return t.FinishReason
}

// Codec understands one stream dialect: it classifies raw events, rewrites
// delta text, and renders the events that end a cut stream. Codecs are
// stateful and must be used for a single stream: Decode remembers envelope
// facts (ids, models, timestamps, sequence numbers) and Track remembers
// what has reached the client (open output items, text emitted so far,
// finished choices) so Terminate can close the stream consistently.
type Codec interface {
	// Decode classifies raw; anything not understood is KindOther. Data in
	// the returned event aliases raw.Data.
	Decode(raw RawEvent, seq int) Event
	// Track records ev as emitted to the client. Streams call it for every
	// decoded event they relay, passing the rewritten event when the text
	// was changed. A stream that emits nothing before its terminal events
	// (buffering) never calls it.
	Track(ev Event)
	// RewriteText returns a copy of ev whose delta text is replaced with
	// text; every other member of the payload is preserved.
	RewriteText(ev Event, text string) (Event, error)
	// Terminate renders the final bytes that end a cut stream, [DONE]
	// included.
	Terminate(t Termination) [][]byte
}

// ErrNotTextEvent is returned by RewriteText for events without delta text.
var ErrNotTextEvent = errors.New("streaming: event carries no rewritable text")

// isDone reports whether raw carries the [DONE] sentinel.
func isDone(raw RawEvent) bool {
	return bytes.Equal(bytes.TrimSpace(raw.Data), donePayload)
}

func decodeOther(raw RawEvent, seq int) Event {
	kind := KindOther
	if raw.Data != nil && isDone(raw) {
		kind = KindDone
	}
	return Event{Seq: seq, Kind: kind, Name: raw.Name, Data: raw.Data}
}

// encodeJSONEvent marshals payload as one SSE event.
func encodeJSONEvent(name string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("streaming: marshal %s event: %w", name, err)
	}
	ev := Event{Name: name, Data: data}
	return ev.Encode(), nil
}

var doneEventBytes = []byte("data: [DONE]\n\n")

// jsonStringOf decodes raw when it is a JSON string.
func jsonStringOf(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

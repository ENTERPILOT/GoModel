package usage

import (
	"bytes"
	"encoding/json"
	"sync/atomic"
)

// realtimeInputAudioMarker is the cheap prefilter for the client event that
// appends audio to a realtime input buffer: it is a suffix of both spellings in
// use, so a frame without it cannot be an append. realtimeInputAudioEvents are
// the exact event names — conversation and transcription sessions send the bare
// one, translation sessions the namespaced one — that a frame must carry to be
// billed.
var (
	realtimeInputAudioMarker = []byte(`input_audio_buffer.append`)
	realtimeInputAudioEvents = [][]byte{
		[]byte(`input_audio_buffer.append`),
		[]byte(`session.input_audio_buffer.append`),
	}
)

// RealtimeInputAudioMeter accumulates the audio a client streams into a realtime
// session. It exists for session types that report no usage of their own —
// OpenAI translation sessions emit only transcript and audio deltas — where the
// relayed audio is the gateway's only measure of what the provider billed.
//
// Observe runs inline on the relay hot path and allocates nothing: a cheap marker
// scan skips every event that is not an append, and an append's byte count comes
// from its base64 payload length rather than from decoding it. What it does check
// is that the frame is one the provider will accept, because audio the provider
// rejects is audio the session never received. The zero value is ready to use,
// and the counter is atomic because Observe runs on the relay goroutine while
// Seconds is read after the session ends.
type RealtimeInputAudioMeter struct {
	audioBytes atomic.Int64
}

// Observe records the audio carried by one client frame, ignoring frames that
// are not input audio appends.
func (m *RealtimeInputAudioMeter) Observe(frame []byte) {
	if !bytes.Contains(frame, realtimeInputAudioMarker) {
		return
	}
	// The marker can appear anywhere in a frame — inside a transcript, a prompt,
	// an item id — so the event type decides. Billing on the marker alone would
	// charge for audio the session never received.
	eventType, ok := topLevelStringField(frame, "type")
	if !ok || !isRealtimeInputAudioEvent(eventType) {
		return
	}
	encoded, ok := topLevelStringField(frame, "audio")
	if !ok {
		return
	}
	// A payload the provider will reject ("invalid_base64") never reaches the
	// session's input buffer, so it is not audio the caller received and must not
	// be billed. Translation sessions acknowledge nothing, so a well-formed
	// payload the gateway relayed is the closest measure of what was accepted.
	decoded, ok := base64DecodedLen(encoded)
	if !ok {
		return
	}
	// Last, because it is the only check that reads the whole frame again: a
	// frame the provider cannot parse is rejected whole, audio included. It
	// allocates nothing, and only frames about to be billed reach it.
	if !json.Valid(frame) {
		return
	}
	m.audioBytes.Add(int64(decoded))
}

// isRealtimeInputAudioEvent reports whether an event type names an input audio
// append.
func isRealtimeInputAudioEvent(eventType []byte) bool {
	for _, name := range realtimeInputAudioEvents {
		if bytes.Equal(eventType, name) {
			return true
		}
	}
	return false
}

// Seconds returns the metered audio duration. Realtime input is PCM16 mono at
// 24 kHz — the format OpenAI's realtime endpoints default to and the only one
// translation sessions accept — so the byte count converts directly.
func (m *RealtimeInputAudioMeter) Seconds() float64 {
	return float64(m.audioBytes.Load()) / pcmBytesPerSecond
}

// topLevelStringField returns the raw value of a top-level string field without
// unmarshaling the frame, which would copy every audio payload the relay carries.
// Only the event object's own fields count: a nested "audio" belongs to an item
// or a session config, not to the append event, and billing it would charge for
// audio the session never received. Like a decoder, a repeated field takes its
// last value; anything that does not scan as an object of key/value pairs
// reports false.
func topLevelStringField(frame []byte, field string) ([]byte, bool) {
	i := skipJSONSpace(frame, 0)
	if i >= len(frame) || frame[i] != '{' {
		return nil, false
	}
	i++
	var value []byte
	found := false
	for {
		i = skipJSONSpace(frame, i)
		if i >= len(frame) {
			return nil, false
		}
		if frame[i] == '}' {
			return value, found
		}
		if frame[i] != '"' {
			return nil, false
		}
		key, next, ok := scanJSONString(frame, i)
		if !ok {
			return nil, false
		}
		i = skipJSONSpace(frame, next)
		if i >= len(frame) || frame[i] != ':' {
			return nil, false
		}
		i = skipJSONSpace(frame, i+1)
		if i >= len(frame) {
			return nil, false
		}
		if string(key) == field {
			// A repeat of the field replaces what came before, string or not:
			// the provider reads the last one too.
			value, found = nil, false
			if frame[i] == '"' {
				if value, next, ok = scanJSONString(frame, i); !ok {
					return nil, false
				}
				found = true
				i = next
			} else if i, ok = skipJSONValue(frame, i); !ok {
				return nil, false
			}
		} else if i, ok = skipJSONValue(frame, i); !ok {
			return nil, false
		}
		i = skipJSONSpace(frame, i)
		if i >= len(frame) {
			return nil, false
		}
		if frame[i] == ',' {
			i++
		} else if frame[i] != '}' {
			return nil, false
		}
	}
}

// scanJSONString returns the raw contents of the string starting at i (which
// must hold its opening quote) and the index just past its closing quote.
//
// Escape sequences are kept verbatim rather than decoded, which costs a copy of
// every audio payload. A frame that escapes a base64 character (JSON permits
// "\/" for "/") is therefore skipped rather than billed — the safe direction,
// and unreachable in practice: no mainstream encoder escapes base64, and an
// escaped event name would not even pass the marker prefilter.
func scanJSONString(frame []byte, i int) ([]byte, int, bool) {
	for j := i + 1; j < len(frame); j++ {
		switch frame[j] {
		case '\\':
			j++ // the escaped character cannot end the string
		case '"':
			return frame[i+1 : j], j + 1, true
		}
	}
	return nil, 0, false
}

// skipJSONValue returns the index just past the value starting at i.
func skipJSONValue(frame []byte, i int) (int, bool) {
	switch frame[i] {
	case '"':
		_, next, ok := scanJSONString(frame, i)
		return next, ok
	case '{', '[':
		depth := 0
		for j := i; j < len(frame); j++ {
			switch frame[j] {
			case '"':
				_, next, ok := scanJSONString(frame, j)
				if !ok {
					return 0, false
				}
				j = next - 1
			case '{', '[':
				depth++
			case '}', ']':
				if depth--; depth == 0 {
					return j + 1, true
				}
			}
		}
		return 0, false
	default: // number, true, false, null: ends at the next structural character
		for j := i; j < len(frame); j++ {
			switch frame[j] {
			case ',', '}', ']', ' ', '\t', '\r', '\n':
				return j, true
			}
		}
		return 0, false
	}
}

func skipJSONSpace(frame []byte, i int) int {
	for ; i < len(frame); i++ {
		switch frame[i] {
		case ' ', '\t', '\r', '\n':
		default:
			return i
		}
	}
	return i
}

// base64DecodedLen returns the number of bytes a base64 payload decodes to,
// without decoding it: every four encoded characters carry three bytes. It
// reports false for anything a decoder would reject — a stray character, a
// length that leaves a single dangling character, misplaced or excess padding,
// or the two alphabets mixed — because the provider answers such a payload with
// an invalid_base64 error and never adds it to the input buffer.
func base64DecodedLen(encoded []byte) (int, bool) {
	body := encoded
	pad := 0
	for pad < 2 && len(body) > 0 && body[len(body)-1] == '=' {
		pad++
		body = body[:len(body)-1]
	}
	// Padding exists only to complete the final four-character quantum, so it
	// must do exactly that; without it the tail may be short but never dangling.
	if pad > 0 && (len(body)+pad)%4 != 0 {
		return 0, false
	}
	if pad == 0 && len(body)%4 == 1 {
		return 0, false
	}
	standard, urlSafe := false, false
	for _, c := range body {
		switch c {
		case '+', '/':
			standard = true
		case '-', '_':
			urlSafe = true
		default:
			if !base64Alphanumeric[c] {
				return 0, false
			}
		}
	}
	if standard && urlSafe {
		return 0, false // no decoder accepts a payload drawn from both alphabets
	}
	return len(body)/4*3 + len(body)%4*3/4, true
}

// base64Alphanumeric marks the characters both base64 alphabets share; the four
// that differ are checked separately so a mixed payload is caught.
var base64Alphanumeric = func() [256]bool {
	var table [256]bool
	for _, c := range []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789") {
		table[c] = true
	}
	return table
}()

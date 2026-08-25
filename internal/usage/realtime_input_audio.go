package usage

import (
	"bytes"
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
// Observe runs inline on the relay hot path, so it never decodes audio: the
// base64 payload length alone gives the byte count, and a cheap marker scan
// skips every other event. The zero value is ready to use, and the counter is
// atomic because Observe runs on the relay goroutine while Seconds is read after
// the session ends.
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
	eventType, ok := jsonStringField(frame, "type")
	if !ok || !isRealtimeInputAudioEvent(eventType) {
		return
	}
	encoded, ok := jsonStringField(frame, "audio")
	if !ok {
		return
	}
	// A payload the provider will reject ("invalid_base64") never reaches the
	// session's input buffer, so it is not audio the caller received and must not
	// be billed. Translation sessions acknowledge nothing, so a well-formed
	// payload the gateway relayed is the closest measure of what was accepted.
	if decoded, ok := base64DecodedLen(encoded); ok {
		m.audioBytes.Add(int64(decoded))
	}
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

// jsonStringField returns the raw value of a JSON string field without
// unmarshaling the frame. Realtime audio payloads are base64, which contains no
// quotes or escapes, so the value ends at the next quote. It reports false when
// the field is absent or is not a string (e.g. the "audio" object of a
// session.update frame).
func jsonStringField(frame []byte, field string) ([]byte, bool) {
	key := []byte(`"` + field + `"`)
	rest := frame
	for {
		idx := bytes.Index(rest, key)
		if idx < 0 {
			return nil, false
		}
		value := rest[idx+len(key):]
		rest = value
		// Skip the whitespace and colon a JSON encoder may or may not emit.
		value = bytes.TrimLeft(value, " \t\r\n")
		if len(value) == 0 || value[0] != ':' {
			continue
		}
		value = bytes.TrimLeft(value[1:], " \t\r\n")
		if len(value) == 0 || value[0] != '"' {
			continue // not a string value: keep looking for the audio payload
		}
		if end := bytes.IndexByte(value[1:], '"'); end >= 0 {
			return value[1 : 1+end], true
		}
		return nil, false
	}
}

// base64DecodedLen returns the number of bytes a base64 payload decodes to,
// without decoding it: every four encoded characters carry three bytes, less one
// byte per padding character. It reports false for a payload no decoder accepts
// — a stray character, or a length that leaves a single dangling character —
// which is what the provider answers with an invalid_base64 error.
func base64DecodedLen(encoded []byte) (int, bool) {
	trimmed := bytes.TrimRight(encoded, "=")
	if len(trimmed)%4 == 1 {
		return 0, false
	}
	for _, c := range trimmed {
		if !base64Alphabet[c] {
			return 0, false
		}
	}
	return len(trimmed)/4*3 + len(trimmed)%4*3/4, true
}

// base64Alphabet marks the characters both base64 alphabets use, so the scan
// accepts standard and URL-safe payloads alike (Postel) while rejecting anything
// that is not base64 at all.
var base64Alphabet = func() [256]bool {
	var table [256]bool
	for _, c := range []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/-_") {
		table[c] = true
	}
	return table
}()

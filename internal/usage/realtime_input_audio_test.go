package usage

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"testing"
)

// appendFrame builds a client audio append event carrying n bytes of PCM16.
func appendFrame(t *testing.T, eventType string, n int) []byte {
	t.Helper()
	frame, err := json.Marshal(map[string]any{
		"type":  eventType,
		"audio": base64.StdEncoding.EncodeToString(make([]byte, n)),
	})
	if err != nil {
		t.Fatalf("failed to build frame: %v", err)
	}
	return frame
}

func TestRealtimeInputAudioMeterSeconds(t *testing.T) {
	tests := []struct {
		name        string
		eventType   string
		frames      int
		bytesPerAdd int
		wantSeconds float64
	}{
		// Conversation and transcription sessions use the bare event name,
		// translation sessions namespace it under "session.".
		{name: "conversation append", eventType: "input_audio_buffer.append", frames: 10, bytesPerAdd: 4800, wantSeconds: 1},
		{name: "translation append", eventType: "session.input_audio_buffer.append", frames: 10, bytesPerAdd: 4800, wantSeconds: 1},
		// A chunk length that is not a multiple of 3 exercises base64 padding.
		{name: "padded payload", eventType: "session.input_audio_buffer.append", frames: 1, bytesPerAdd: 48001, wantSeconds: 48001.0 / 48000.0},
		{name: "no audio at all", eventType: "session.input_audio_buffer.append", frames: 0, wantSeconds: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var meter RealtimeInputAudioMeter
			for range tt.frames {
				meter.Observe(appendFrame(t, tt.eventType, tt.bytesPerAdd))
			}
			if got := meter.Seconds(); math.Abs(got-tt.wantSeconds) > 1e-9 {
				t.Errorf("Seconds() = %v, want %v", got, tt.wantSeconds)
			}
		})
	}
}

func TestRealtimeInputAudioMeterIgnoresOtherFrames(t *testing.T) {
	// Only audio appends are billable: session config carries an "audio" object
	// rather than a payload, and the server's own audio deltas are output, which
	// the client never sends.
	frames := [][]byte{
		[]byte(`{"type":"session.update","session":{"audio":{"output":{"language":"es"}}}}`),
		[]byte(`{"type":"session.input_audio_buffer.commit"}`),
		[]byte(`{"type":"session.output_audio.delta","delta":"AAAAAAAA"}`),
		[]byte(`not json at all`),
		{},
	}
	var meter RealtimeInputAudioMeter
	for _, frame := range frames {
		meter.Observe(frame)
	}
	if got := meter.Seconds(); got != 0 {
		t.Errorf("Seconds() = %v, want 0 for frames that carry no input audio", got)
	}
}

func TestRealtimeInputAudioMeterReadsAudioAfterOtherFields(t *testing.T) {
	// Field order is the client's choice, and an append may carry an event id, so
	// the scan must find the audio payload wherever it sits.
	frame := []byte(`{"event_id":"evt_1","type":"session.input_audio_buffer.append","audio":"` +
		base64.StdEncoding.EncodeToString(make([]byte, 48000)) + `"}`)
	var meter RealtimeInputAudioMeter
	meter.Observe(frame)
	if got := meter.Seconds(); math.Abs(got-1) > 1e-9 {
		t.Errorf("Seconds() = %v, want 1", got)
	}
}

func TestRealtimeInputAudioMeterIgnoresMarkerOutsideEventType(t *testing.T) {
	// The append marker can ride along inside any field — a transcript, a prompt,
	// an item id. Only the event type makes a frame billable, so a frame that
	// merely mentions the marker while carrying an "audio" string must not be
	// charged.
	payload := base64.StdEncoding.EncodeToString(make([]byte, 48000))
	frames := [][]byte{
		[]byte(`{"type":"conversation.item.create","item":{"content":[{"type":"input_text","text":"send input_audio_buffer.append frames"}],"audio":"` + payload + `"}}`),
		[]byte(`{"type":"session.update","session":{"instructions":"input_audio_buffer.append"},"audio":"` + payload + `"}`),
		// A near-miss event name is not the append event either.
		[]byte(`{"type":"custom.input_audio_buffer.append","audio":"` + payload + `"}`),
		// No event type at all: nothing identifies the frame as an append.
		[]byte(`{"note":"input_audio_buffer.append","audio":"` + payload + `"}`),
	}
	var meter RealtimeInputAudioMeter
	for _, frame := range frames {
		meter.Observe(frame)
	}
	if got := meter.Seconds(); got != 0 {
		t.Errorf("Seconds() = %v, want 0 for frames that only mention the append event", got)
	}
}

func TestRealtimeInputAudioMeterIgnoresUndecodablePayloads(t *testing.T) {
	// The provider answers a malformed payload with invalid_base64 and never adds
	// it to the input buffer, so the gateway must not bill it either.
	valid := base64.StdEncoding.EncodeToString(make([]byte, 48000))
	frames := map[string][]byte{
		"stray characters": []byte(`{"type":"session.input_audio_buffer.append","audio":"!!! not base64 !!!"}`),
		"dangling char":    []byte(`{"type":"session.input_audio_buffer.append","audio":"` + valid[:len(valid)-3] + `"}`),
		"escaped quote":    []byte(`{"type":"session.input_audio_buffer.append","audio":"AAAA\"BBBB"}`),
		"empty payload":    []byte(`{"type":"session.input_audio_buffer.append","audio":""}`),
	}
	for name, frame := range frames {
		t.Run(name, func(t *testing.T) {
			var meter RealtimeInputAudioMeter
			meter.Observe(frame)
			if got := meter.Seconds(); got != 0 {
				t.Errorf("Seconds() = %v, want 0 for a payload the provider rejects", got)
			}
		})
	}
}

func TestBase64DecodedLen(t *testing.T) {
	// The gateway must agree with the decoders the provider uses: a payload it
	// accepts is billed, and one any decoder rejects is not.
	tests := map[string]struct {
		encoded string
		want    int
		wantOK  bool
	}{
		"padded":          {encoded: base64.StdEncoding.EncodeToString(make([]byte, 100)), want: 100, wantOK: true},
		"unpadded":        {encoded: base64.RawStdEncoding.EncodeToString(make([]byte, 100)), want: 100, wantOK: true},
		"url safe":        {encoded: base64.RawURLEncoding.EncodeToString([]byte{0xfb, 0xff, 0xbf}), want: 3, wantOK: true},
		"url safe padded": {encoded: base64.URLEncoding.EncodeToString([]byte{0xfb, 0xff, 0xbf, 0x01}), want: 4, wantOK: true},
		"one leftover":    {encoded: "AAAAA", wantOK: false},
		"not base64":      {encoded: "hello world!", wantOK: false},
		// Padding completes the final quantum; anywhere else no decoder accepts it.
		"padding on a full quantum": {encoded: "AAAA=", wantOK: false},
		"excess padding":            {encoded: "AA===", wantOK: false},
		"four padding characters":   {encoded: "AAAA====", wantOK: false},
		"short padded":              {encoded: "AA=", wantOK: false},
		"padding inside":            {encoded: "AA=AAAAA", wantOK: false},
		// The two alphabets are alternatives, never a mixture.
		"mixed alphabets": {encoded: "AA+_", wantOK: false},
		"whitespace":      {encoded: "AAAA AAAA", wantOK: false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, ok := base64DecodedLen([]byte(tt.encoded))
			if ok != tt.wantOK || (ok && got != tt.want) {
				t.Errorf("base64DecodedLen(%q) = (%d, %v), want (%d, %v)", tt.encoded, got, ok, tt.want, tt.wantOK)
			}
			if decodable := anyBase64Decodes(tt.encoded); decodable != tt.wantOK {
				t.Errorf("Go decoders accept %q = %v, but the case expects %v", tt.encoded, decodable, tt.wantOK)
			}
		})
	}
}

// anyBase64Decodes reports whether any standard Go decoder accepts the payload,
// pinning the validator's expectations to real decoder behavior.
func anyBase64Decodes(encoded string) bool {
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if _, err := enc.DecodeString(encoded); err == nil {
			return true
		}
	}
	return false
}

func TestRealtimeInputAudioMeterReadsOnlyTopLevelFields(t *testing.T) {
	// A nested "audio" belongs to an item or a session config, not to the append
	// event, and a nested "type" does not make a frame an append. Billing either
	// would charge for audio the session never received.
	payload := base64.StdEncoding.EncodeToString(make([]byte, 48000))
	frames := map[string]string{
		"nested audio only":     `{"type":"session.input_audio_buffer.append","item":{"audio":"` + payload + `"}}`,
		"nested append type":    `{"item":{"type":"session.input_audio_buffer.append"},"audio":"` + payload + `"}`,
		"append type in a list": `{"types":["session.input_audio_buffer.append"],"audio":"` + payload + `"}`,
		"not an object":         `["session.input_audio_buffer.append","` + payload + `"]`,
		"truncated frame":       `{"type":"session.input_audio_buffer.append","audio":"` + payload,
	}
	for name, frame := range frames {
		t.Run(name, func(t *testing.T) {
			var meter RealtimeInputAudioMeter
			meter.Observe([]byte(frame))
			if got := meter.Seconds(); got != 0 {
				t.Errorf("Seconds() = %v, want 0 for audio outside the append event", got)
			}
		})
	}
}

func TestRealtimeInputAudioMeterReadsAppendsAroundOtherFields(t *testing.T) {
	// A real append may carry an event id and nested siblings, in any order, with
	// whatever spacing the client's encoder emits: the payload is still billable.
	payload := base64.StdEncoding.EncodeToString(make([]byte, 48000))
	frames := map[string]string{
		"audio last":        `{"event_id":"evt_1","type":"session.input_audio_buffer.append","audio":"` + payload + `"}`,
		"audio first":       `{"audio":"` + payload + `","type":"session.input_audio_buffer.append"}`,
		"nested sibling":    `{"type":"session.input_audio_buffer.append","meta":{"seq":3,"tags":["a"]},"audio":"` + payload + `"}`,
		"escaped sibling":   `{"label":"a \"session.input_audio_buffer.append\" frame","type":"input_audio_buffer.append","audio":"` + payload + `"}`,
		"whitespace around": "{ \"type\" : \"input_audio_buffer.append\" , \"audio\" : \"" + payload + "\" }",
	}
	for name, frame := range frames {
		t.Run(name, func(t *testing.T) {
			var meter RealtimeInputAudioMeter
			meter.Observe([]byte(frame))
			if got := meter.Seconds(); math.Abs(got-1) > 1e-9 {
				t.Errorf("Seconds() = %v, want 1", got)
			}
		})
	}
}

func TestTopLevelStringFieldMatchesJSONDecoding(t *testing.T) {
	// The scanner replaces unmarshaling to keep audio payloads off the hot path,
	// so its answers must be the ones a real JSON decode would give.
	frames := []string{
		`{"type":"input_audio_buffer.append","audio":"QUJD"}`,
		`{"audio":"QUJD","type":"input_audio_buffer.append"}`,
		`{ "type" : "x" , "audio" : "QUJD" }`,
		`{"type":"x","item":{"audio":"QUJD"},"audio":"REVG"}`,
		`{"type":"x","audio":{"format":"pcm16"}}`,
		`{"type":"x","n":12,"ok":true,"nil":null,"audio":"QUJD"}`,
		`{"type":"x","list":[1,{"audio":"nope"},"]"],"audio":"QUJD"}`,
		`{"quote\"key":"v","audio":"QUJD"}`,
		`{"audio":"QUJD","audio":"REVGRw"}`,
		`{"audio":{"nested":true},"audio":"QUJD"}`,
		`{"audio":"QUJD","audio":{"nested":true}}`,
		`{"type":"x"}`,
		`{}`,
		`[]`,
		`{"type":"x","audio":"QUJD"`,
		`not json`,
		``,
	}
	for _, frame := range frames {
		t.Run(frame, func(t *testing.T) {
			if !json.Valid([]byte(frame)) {
				// Invalid frames never reach the scanner's answer: Observe drops
				// them, which TestRealtimeInputAudioMeterReadsOnlyTopLevelFields
				// covers. Only the parse of a real object is compared here.
				return
			}
			for _, field := range []string{"type", "audio"} {
				want, wantOK := referenceStringField(frame, field)
				got, gotOK := topLevelStringField([]byte(frame), field)
				if gotOK != wantOK || (gotOK && string(got) != want) {
					t.Errorf("topLevelStringField(%q) = (%q, %v), want (%q, %v)", field, got, gotOK, want, wantOK)
				}
			}
		})
	}
}

// referenceStringField reads a top-level string field the obvious way, by
// unmarshaling. Escapes stay raw so it matches what the scanner returns.
func referenceStringField(frame, field string) (string, bool) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(frame), &object); err != nil {
		return "", false
	}
	raw, ok := object[field]
	if !ok || len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	return string(raw[1 : len(raw)-1]), true
}

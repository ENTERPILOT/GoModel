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

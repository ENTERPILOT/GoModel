package server

import (
	"encoding/json"
	"testing"
)

func TestPinTranscriptionModel(t *testing.T) {
	pin := pinTranscriptionModel("gpt-4o-transcribe")

	tests := []struct {
		name  string
		frame string
		want  string // "" means the frame must pass through unchanged
	}{
		{
			name:  "rewrites a client-chosen model",
			frame: `{"type":"session.update","session":{"type":"transcription","audio":{"input":{"transcription":{"model":"whisper-1","language":"pl"}}}}}`,
			want:  `{"session":{"audio":{"input":{"transcription":{"language":"pl","model":"gpt-4o-transcribe"}}},"type":"transcription"},"type":"session.update"}`,
		},
		{
			name:  "pins when the client omits the model",
			frame: `{"type":"session.update","session":{"type":"transcription"}}`,
			want:  `{"session":{"audio":{"input":{"transcription":{"model":"gpt-4o-transcribe"}}},"type":"transcription"},"type":"session.update"}`,
		},
		{
			name:  "pins the legacy beta field only when the client sent it",
			frame: `{"type":"session.update","session":{"input_audio_transcription":{"model":"whisper-1"}}}`,
			want:  `{"session":{"audio":{"input":{"transcription":{"model":"gpt-4o-transcribe"}}},"input_audio_transcription":{"model":"gpt-4o-transcribe"}},"type":"session.update"}`,
		},
		{
			// A raw byte-marker gate would miss the escaped type and let the
			// frame through unpinned; the decoded type must be what counts.
			name:  "escape-encoded session.update is still pinned",
			frame: `{"type":"session\u002eupdate","session":{"audio":{"input":{"transcription":{"model":"whisper-1"}}}}}`,
			want:  `{"session":{"audio":{"input":{"transcription":{"model":"gpt-4o-transcribe"}}}},"type":"session.update"}`,
		},
		{name: "audio append passes untouched", frame: `{"type":"input_audio_buffer.append","audio":"AAAA"}`},
		{name: "marker in a string value is not a session.update", frame: `{"type":"conversation.item.create","text":"say \"session.update\""}`},
		{name: "session.update without session object passes to upstream to reject", frame: `{"type":"session.update"}`},
		{
			// Pinning must never destroy a client value: a non-object where the
			// transcription config nests is the upstream's error to report.
			name:  "scalar audio passes to upstream to reject",
			frame: `{"type":"session.update","session":{"audio":"bogus"}}`,
		},
		{name: "array input passes to upstream to reject", frame: `{"type":"session.update","session":{"audio":{"input":[1,2]}}}`},
		{name: "scalar transcription passes to upstream to reject", frame: `{"type":"session.update","session":{"audio":{"input":{"transcription":7}}}}`},
		{
			name:  "null audio is treated as absent and pinned",
			frame: `{"type":"session.update","session":{"audio":null}}`,
			want:  `{"session":{"audio":{"input":{"transcription":{"model":"gpt-4o-transcribe"}}}},"type":"session.update"}`,
		},
		{name: "invalid JSON passes to upstream to reject", frame: `{"type":"session.update",`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(pin([]byte(tt.frame)))
			want := tt.want
			if want == "" {
				want = tt.frame
			}
			if !jsonOrRawEqual(got, want) {
				t.Errorf("mapped frame = %s, want %s", got, want)
			}
		})
	}
}

// jsonOrRawEqual compares JSON payloads structurally, falling back to raw string
// comparison for non-JSON frames.
func jsonOrRawEqual(a, b string) bool {
	var av, bv any
	if json.Unmarshal([]byte(a), &av) != nil || json.Unmarshal([]byte(b), &bv) != nil {
		return a == b
	}
	aj, _ := json.Marshal(av)
	bj, _ := json.Marshal(bv)
	return string(aj) == string(bj)
}

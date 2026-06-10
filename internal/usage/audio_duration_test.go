package usage

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// buildWAV produces a canonical PCM WAV of the given duration so duration and
// cost tests can assert exact, dependency-free measurements.
func buildWAV(t *testing.T, sampleRate, channels, bitsPerSample int, seconds float64) []byte {
	t.Helper()
	byteRate := sampleRate * channels * bitsPerSample / 8
	dataLen := int(float64(byteRate) * seconds)

	var buf bytes.Buffer
	write := func(v any) {
		if err := binary.Write(&buf, binary.LittleEndian, v); err != nil {
			t.Fatalf("write wav: %v", err)
		}
	}
	buf.WriteString("RIFF")
	write(uint32(36 + dataLen))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	write(uint32(16))
	write(uint16(1)) // PCM
	write(uint16(channels))
	write(uint32(sampleRate))
	write(uint32(byteRate))
	write(uint16(channels * bitsPerSample / 8))
	write(uint16(bitsPerSample))
	buf.WriteString("data")
	write(uint32(dataLen))
	buf.Write(make([]byte, dataLen))
	return buf.Bytes()
}

func TestMeasureSpeechDurationSeconds(t *testing.T) {
	tests := []struct {
		name   string
		data   []byte
		format string
		want   float64
		wantOK bool
	}{
		{"wav 1s 24kHz mono", buildWAV(t, 24000, 1, 16, 1.0), "wav", 1.0, true},
		{"wav 2.5s 48kHz stereo", buildWAV(t, 48000, 2, 16, 2.5), "wav", 2.5, true},
		{"wav detected despite mp3 format hint", buildWAV(t, 24000, 1, 16, 0.5), "mp3", 0.5, true},
		{"pcm half second", make([]byte, pcmBytesPerSecond/2), "pcm", 0.5, true},
		{"pcm via mime", make([]byte, pcmBytesPerSecond), "audio/pcm", 1.0, true},
		{"mp3 unmeasured", []byte("\xff\xfbnot really mp3"), "mp3", 0, false},
		{"opus unmeasured", []byte("OggS....."), "opus", 0, false},
		{"empty", nil, "wav", 0, false},
		{"truncated riff", []byte("RIFF"), "wav", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := measureSpeechDurationSeconds(tt.data, tt.format)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && !nearlyEqual(got, tt.want) {
				t.Errorf("seconds = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWavDurationSeconds_FallsBackToTrailingBytes(t *testing.T) {
	// Some streamed encoders write an unknown (0xFFFFFFFF) data chunk size; the
	// parser must fall back to the actual trailing byte count.
	wav := buildWAV(t, 24000, 1, 16, 1.0)
	idx := bytes.Index(wav, []byte("data"))
	if idx < 0 {
		t.Fatal("no data chunk")
	}
	binary.LittleEndian.PutUint32(wav[idx+4:idx+8], 0xFFFFFFFF)

	got, ok := wavDurationSeconds(wav)
	if !ok || !nearlyEqual(got, 1.0) {
		t.Errorf("got (%v, %v), want (1, true)", got, ok)
	}
}

func TestWavDurationSeconds_SkipsZeroLengthChunk(t *testing.T) {
	// A valid zero-length non-data chunk (e.g. "fact") before "data" must not
	// stop the walk; the duration is still derived from the following data chunk.
	wav := buildWAV(t, 24000, 1, 16, 1.0)
	idx := bytes.Index(wav, []byte("data"))
	if idx < 0 {
		t.Fatal("no data chunk")
	}
	withEmptyChunk := append(append(append([]byte{}, wav[:idx]...), []byte("fact\x00\x00\x00\x00")...), wav[idx:]...)

	got, ok := wavDurationSeconds(withEmptyChunk)
	if !ok || !nearlyEqual(got, 1.0) {
		t.Errorf("got (%v, %v), want (1, true)", got, ok)
	}
}

func TestNormalizeAudioFormat(t *testing.T) {
	cases := map[string]string{
		"wav":                     "wav",
		"WAV":                     "wav",
		"audio/wav":               "wav",
		"audio/mpeg":              "mp3",
		"mp3":                     "mp3",
		"audio/webm; codecs=opus": "webm",
		"pcm":                     "pcm",
		"audio/l16":               "pcm",
		"":                        "",
	}
	for in, want := range cases {
		if got := normalizeAudioFormat(in); got != want {
			t.Errorf("normalizeAudioFormat(%q) = %q, want %q", in, got, want)
		}
	}
}

func nearlyEqual(a, b float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff < 1e-9
}

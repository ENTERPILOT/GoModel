package usage

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeInChunks feeds w in fixed-size pieces, standing in for the arbitrary
// boundaries a relayed body arrives on.
func writeInChunks(t *testing.T, w interface {
	Write([]byte) (int, error)
}, data []byte, chunk int) {
	t.Helper()
	for start := 0; start < len(data); start += chunk {
		end := min(start+chunk, len(data))
		n, err := w.Write(data[start:end])
		require.NoError(t, err)
		require.Equal(t, end-start, n)
	}
}

// wavWithPreDataChunk builds a valid WAVE container that writes a JUNK chunk of
// the given size before its fmt and data chunks, as alignment padding and
// metadata do in the wild, pushing the audio past the start of the stream.
func wavWithPreDataChunk(t *testing.T, junk int, seconds float64) []byte {
	t.Helper()
	require.Zero(t, junk%2, "RIFF chunks are word-aligned")

	base := buildWAV(t, 24000, 1, 16, seconds)
	chunk := append([]byte("JUNK"), byte(junk), byte(junk>>8), byte(junk>>16), byte(junk>>24))
	chunk = append(chunk, make([]byte, junk)...)

	out := append([]byte(nil), base[:12]...)
	out = append(out, chunk...)
	out = append(out, base[12:]...)
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(out)-8)) // RIFF size
	return out
}

// TestSpeechDurationMeter_MatchesBufferedMeasurement pins the meter to the
// buffered measurement it replaces on the streamed path: the same audio must
// cost the same whether it was relayed or held.
func TestSpeechDurationMeter_MatchesBufferedMeasurement(t *testing.T) {
	tests := []struct {
		name   string
		format string
		audio  []byte
	}{
		{name: "wav", format: "wav", audio: buildWAV(t, 24000, 1, 16, 2)},
		{name: "wav stereo", format: "wav", audio: buildWAV(t, 44100, 2, 16, 0.75)},
		// The audio starts past any fixed-size view of the head, so the header
		// must be read as far as it actually runs.
		{name: "wav behind a 9 kB junk chunk", format: "wav", audio: wavWithPreDataChunk(t, 9000, 2)},
		{name: "pcm", format: "pcm", audio: make([]byte, pcmBytesPerSecond*3)},
		{name: "mp3", format: "mp3", audio: buildMP3(500)},
		{name: "mp3 behind an id3 tag", format: "mp3", audio: append(
			append([]byte{'I', 'D', '3', 4, 0, 0, 0, 0, 0x02, 0x7F}, make([]byte, 383)...),
			buildMP3(120)...)},
		// A wav labelled mp3: the container describes itself, so it wins.
		{name: "format disagrees with the container", format: "mp3", audio: buildWAV(t, 24000, 1, 16, 1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want, wantOK := measureSpeechDurationSeconds(tt.audio, tt.format)
			require.True(t, wantOK, "fixture is not measurable when buffered")

			// Every chunk size exercises a different set of split points,
			// including ones that cut a frame or the container header in half.
			for _, chunk := range []int{1, 7, 64, 191, 192, 193, 1024, 32 * 1024, len(tt.audio)} {
				meter := NewSpeechDurationMeter(tt.format)
				writeInChunks(t, meter, tt.audio, chunk)
				seconds, ok := meter.Seconds()
				assert.True(t, ok, "chunk %d: duration went unmeasured", chunk)
				assert.InDelta(t, want, seconds, 1e-9, "chunk %d", chunk)
			}
		})
	}
}

// TestSpeechDurationMeter_MeasuresPastTheHeaderCapture is the regression this
// type exists for: a response larger than any buffer the gateway keeps is still
// priced by its duration, because only the header is retained.
func TestSpeechDurationMeter_MeasuresPastTheHeaderCapture(t *testing.T) {
	// 200 s at 48 kB/s is 9.6 MB — past speechHeaderCapture and past the audit
	// capture ceiling both.
	audio := buildWAV(t, 24000, 1, 16, 200)
	require.Greater(t, len(audio), speechHeaderCapture)

	meter := NewSpeechDurationMeter("wav")
	writeInChunks(t, meter, audio, 32*1024)

	seconds, ok := meter.Seconds()
	require.True(t, ok)
	assert.InDelta(t, 200.0, seconds, 1e-9)
}

// TestSpeechDurationMeter_StreamedWavWithoutADeclaredSize covers the header a
// streaming encoder writes when it does not yet know the length.
func TestSpeechDurationMeter_StreamedWavWithoutADeclaredSize(t *testing.T) {
	for _, declared := range []uint32{0, 0xFFFFFFFF} {
		t.Run(fmt.Sprintf("declared %d", declared), func(t *testing.T) {
			audio := buildWAV(t, 24000, 1, 16, 4)
			// The data chunk size is the last header field before the samples.
			size := []byte{byte(declared), byte(declared >> 8), byte(declared >> 16), byte(declared >> 24)}
			copy(audio[40:44], size)

			meter := NewSpeechDurationMeter("wav")
			writeInChunks(t, meter, audio, 4096)

			seconds, ok := meter.Seconds()
			require.True(t, ok)
			assert.InDelta(t, 4.0, seconds, 1e-9)
		})
	}
}

// TestSpeechDurationMeter_UnmeasurableFormats keeps an undecodable codec
// reporting a caveat rather than a silent zero, as the buffered path does.
func TestSpeechDurationMeter_UnmeasurableFormats(t *testing.T) {
	for _, format := range []string{"opus", "aac", "flac", ""} {
		t.Run("format "+format, func(t *testing.T) {
			meter := NewSpeechDurationMeter(format)
			writeInChunks(t, meter, make([]byte, 64*1024), 4096)
			seconds, ok := meter.Seconds()
			assert.False(t, ok)
			assert.Zero(t, seconds)
		})
	}
}

// TestSpeechDurationMeter_EmptyStream reports nothing measurable for a provider
// that produced no audio at all.
func TestSpeechDurationMeter_EmptyStream(t *testing.T) {
	meter := NewSpeechDurationMeter("wav")
	seconds, ok := meter.Seconds()
	assert.False(t, ok)
	assert.Zero(t, seconds)
}

// TestSpeechDurationMeter_MP3RetainsAtMostOneFrame pins the walker's memory
// bound, which is what lets a long mp3 be measured at all: every byte is either
// counted into a frame or discarded, so neither a stream that never looks like
// audio nor a very long one accumulates.
func TestSpeechDurationMeter_MP3RetainsAtMostOneFrame(t *testing.T) {
	const maxLayerIIIFrame = 1441

	tests := map[string][]byte{
		"never syncs": bytes.Repeat([]byte{0x00}, 512*1024),
		"long stream": buildMP3(20000),
		"junk then frames": append(bytes.Repeat([]byte{0x00}, 64*1024),
			buildMP3(100)...),
	}

	for name, audio := range tests {
		t.Run(name, func(t *testing.T) {
			meter := NewSpeechDurationMeter("mp3")
			for start := 0; start < len(audio); start += 8192 {
				end := min(start+8192, len(audio))
				_, err := meter.Write(audio[start:end])
				require.NoError(t, err)
				assert.LessOrEqual(t, len(meter.mp3.carry), maxLayerIIIFrame+3,
					"walker retained %d bytes at offset %d", len(meter.mp3.carry), start)
			}
			assert.LessOrEqual(t, len(meter.head), speechHeaderCapture)
		})
	}
}

// transcriptEvents renders a streamed transcript: delta events closed by the
// usage-bearing done event.
func transcriptEvents(deltas int, usageJSON string) string {
	var b strings.Builder
	for i := range deltas {
		fmt.Fprintf(&b, "data: {\"type\":\"transcript.text.delta\",\"delta\":\"word%d \"}\n\n", i)
	}
	fmt.Fprintf(&b, "data: {\"type\":\"transcript.text.done\",\"text\":\"done\",\"usage\":%s}\n\n", usageJSON)
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func TestTranscriptUsageCollector(t *testing.T) {
	const usageJSON = `{"type":"tokens","input_tokens":196,"output_tokens":52,"total_tokens":248}`

	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "keeps the terminal usage event",
			body: transcriptEvents(3, usageJSON),
			want: `{"type":"transcript.text.done","text":"done","usage":` + usageJSON + `}`,
		},
		{
			name: "keeps the last usage event when several carry one",
			body: "data: {\"usage\":{\"seconds\":1}}\n\n" + transcriptEvents(1, usageJSON),
			want: `{"type":"transcript.text.done","text":"done","usage":` + usageJSON + `}`,
		},
		{
			name: "passes a buffering provider's json body through",
			body: `{"text":"hello","usage":` + usageJSON + `}`,
			want: `{"text":"hello","usage":` + usageJSON + `}`,
		},
		{
			name: "keeps a final event that has no trailing newline",
			body: "data: {\"type\":\"transcript.text.done\",\"usage\":" + usageJSON + "}",
			want: `{"type":"transcript.text.done","usage":` + usageJSON + `}`,
		},
		{
			name: "reports nothing for a transcript without usage",
			body: "data: {\"type\":\"transcript.text.delta\",\"delta\":\"hi\"}\n\ndata: [DONE]\n\n",
			want: "",
		},
		{
			name: "ignores an event that is not json",
			body: "data: [DONE]\n\nevent: ping\n\n",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, chunk := range []int{1, 3, 17, 64, len(tt.body)} {
				collector := NewTranscriptUsageCollector(8 * 1024 * 1024)
				writeInChunks(t, collector, []byte(tt.body), chunk)
				assert.Equal(t, tt.want, string(collector.UsageBody()), "chunk %d", chunk)
			}
		})
	}
}

// TestTranscriptUsageCollector_PricesPastTheAuditCeiling is the transcript half
// of the same regression: an enormous transcript still carries its own usage.
func TestTranscriptUsageCollector_PricesPastTheAuditCeiling(t *testing.T) {
	const cap = 64 * 1024
	const usageJSON = `{"type":"tokens","input_tokens":196,"output_tokens":52,"total_tokens":248}`
	body := transcriptEvents(20000, usageJSON) // comfortably past the cap
	require.Greater(t, len(body), cap)

	collector := NewTranscriptUsageCollector(cap)
	writeInChunks(t, collector, []byte(body), 4096)

	assert.Equal(t,
		`{"type":"transcript.text.done","text":"done","usage":`+usageJSON+`}`,
		string(collector.UsageBody()))
}

// TestTranscriptUsageCollector_OversizeJSONBody bounds the one shape that has to
// be buffered whole: a provider that ignored stream=true and answered with a
// single object. Past the cap the call falls back to the upload's duration.
func TestTranscriptUsageCollector_OversizeJSONBody(t *testing.T) {
	const cap = 4096
	body := `{"text":"` + strings.Repeat("a", 2*cap) + `"}`

	collector := NewTranscriptUsageCollector(cap)
	writeInChunks(t, collector, []byte(body), 512)

	assert.Nil(t, collector.UsageBody())
}

// TestTranscriptUsageCollector_OversizeSingleEvent bounds an event line that
// never ends, so a malformed stream cannot grow the collector without limit.
func TestTranscriptUsageCollector_OversizeSingleEvent(t *testing.T) {
	const cap = 4096
	body := "data: {\"usage\":{\"seconds\":1},\"pad\":\"" + strings.Repeat("a", 4*cap) + "\"}\n"

	collector := NewTranscriptUsageCollector(cap)
	writeInChunks(t, collector, []byte(body), 512)

	assert.Nil(t, collector.UsageBody())
	assert.Zero(t, collector.line.Len())
}

// TestExtractFromStreamedSpeechRequest_PricesMeteredDuration checks the entry a
// relayed speech response produces carries the metered duration, so a
// per-second-output model is billed.
func TestExtractFromStreamedSpeechRequest_PricesMeteredDuration(t *testing.T) {
	meter := NewSpeechDurationMeter("wav")
	writeInChunks(t, meter, buildWAV(t, 24000, 1, 16, 2.5), 4096)

	entry := ExtractFromStreamedSpeechRequest("hello", meter, "wav", "req-1", "gpt-4o-mini-tts", "openai")

	require.NotNil(t, entry)
	require.NotNil(t, entry.RawData)
	assert.Equal(t, 2.5, entry.RawData[rawKeyAudioOutputSeconds])
	assert.Equal(t, 5, entry.RawData[rawKeyInputCharacters])
	assert.Equal(t, "wav", entry.RawData[rawKeyAudioOutputFormat])
}

// TestExtractFromStreamedSpeechRequest_UnmeasurableFormat leaves the duration
// out so cost.go raises its caveat instead of billing zero seconds.
func TestExtractFromStreamedSpeechRequest_UnmeasurableFormat(t *testing.T) {
	meter := NewSpeechDurationMeter("opus")
	writeInChunks(t, meter, make([]byte, 4096), 1024)

	entry := ExtractFromStreamedSpeechRequest("hello", meter, "opus", "req-1", "gpt-4o-mini-tts", "openai")

	require.NotNil(t, entry)
	assert.NotContains(t, entry.RawData, rawKeyAudioOutputSeconds)
	assert.Equal(t, "opus", entry.RawData[rawKeyAudioOutputFormat])
}

// TestSpeechDurationMeter_PreDataChunkPastTheCeiling pins the documented bound on
// reading a container header: a stream whose audio starts beyond
// speechHeaderCapture is reported unmeasurable, so the call takes a cost caveat
// rather than the meter retaining the response to keep looking.
func TestSpeechDurationMeter_PreDataChunkPastTheCeiling(t *testing.T) {
	audio := wavWithPreDataChunk(t, 4*speechHeaderCapture, 1)

	meter := NewSpeechDurationMeter("wav")
	writeInChunks(t, meter, audio, 64*1024)

	seconds, ok := meter.Seconds()
	assert.False(t, ok)
	assert.Zero(t, seconds)
	assert.Nil(t, meter.head, "the head was retained past the ceiling")

	// The same container measures when it is buffered, which is why the ceiling
	// is set well beyond any header a real encoder writes.
	buffered, bufferedOK := measureSpeechDurationSeconds(audio, "wav")
	require.True(t, bufferedOK)
	assert.InDelta(t, 1.0, buffered, 1e-9)
}

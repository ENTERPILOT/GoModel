package usage

import (
	"encoding/binary"
	"strings"
)

// pcmBytesPerSecond is the byte rate of OpenAI's headerless PCM speech output:
// signed 16-bit, little-endian, mono, 24 kHz (24000 samples * 2 bytes).
const pcmBytesPerSecond = 24000 * 2

// measureSpeechDurationSeconds returns the playback duration, in seconds, of
// synthesized speech the gateway can compute without decoding audio. It parses
// self-describing WAV (RIFF/WAVE) containers, the fixed-rate PCM stream OpenAI
// emits for response_format=pcm, and MPEG (mp3) streams via their frame
// headers — mp3 matters because it is OpenAI's default speech format, so
// per-second-output models would otherwise always cost zero. Other compressed
// formats (opus, aac, flac) return ok=false so the caller can flag the cost as
// partial rather than reporting a silent zero.
func measureSpeechDurationSeconds(data []byte, format string) (float64, bool) {
	if seconds, ok := wavDurationSeconds(data); ok {
		return seconds, true
	}
	switch normalizeAudioFormat(format) {
	case "pcm":
		if len(data) > 0 {
			return float64(len(data)) / pcmBytesPerSecond, true
		}
	case "mp3":
		return mp3DurationSeconds(data)
	}
	return 0, false
}

// normalizeAudioFormat reduces a response_format token or audio MIME type to a
// bare lowercase codec name (e.g. "audio/wav" -> "wav", "audio/mpeg" -> "mp3").
func normalizeAudioFormat(format string) string {
	f := strings.ToLower(strings.TrimSpace(format))
	if f == "" {
		return ""
	}
	// Strip MIME parameters, e.g. "audio/webm; codecs=opus".
	f = strings.TrimSpace(strings.Split(f, ";")[0])
	switch f {
	case "audio/wav", "audio/x-wav", "audio/wave":
		return "wav"
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	case "audio/opus", "audio/ogg":
		return "opus"
	case "audio/aac":
		return "aac"
	case "audio/flac", "audio/x-flac":
		return "flac"
	case "audio/pcm", "audio/l16", "audio/basic":
		return "pcm"
	}
	return strings.TrimPrefix(f, "audio/")
}

// mp3SamplesPerFrame and mp3BitrateKbps index Layer III frame parameters by
// MPEG version group: 0 = MPEG-1, 1 = MPEG-2/2.5 (half sample rate).
var (
	mp3SamplesPerFrame = [2]int{1152, 576}
	mp3BitrateKbps     = [2][15]int{
		{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320},
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},
	}
	mp3SampleRates = map[byte][3]int{
		3: {44100, 48000, 32000}, // MPEG-1
		2: {22050, 24000, 16000}, // MPEG-2
		0: {11025, 12000, 8000},  // MPEG-2.5
	}
)

// mp3DurationSeconds derives the playback duration of an MPEG Layer III (mp3)
// stream by walking its frame headers and summing samples-per-frame over the
// sample rate — exact for both CBR and VBR without decoding any audio. It
// skips a leading ID3v2 tag, resyncs across stray bytes, and stops at trailing
// non-frame data (e.g. an ID3v1 tag). Returns ok=false when no frame is found.
func mp3DurationSeconds(data []byte) (float64, bool) {
	pos := skipID3v2(data)

	var seconds float64
	frames := 0
	for pos+4 <= len(data) {
		frameSize, frameSeconds, ok := parseMP3FrameHeader(data[pos:])
		if !ok {
			if frames > 0 {
				break // trailing tag or junk after the audio stream
			}
			pos++ // still hunting for the first sync word
			continue
		}
		if pos+frameSize > len(data) {
			break // truncated frame: do not bill audio that is not there
		}
		seconds += frameSeconds
		frames++
		pos += frameSize
	}
	return seconds, frames > 0
}

// parseMP3FrameHeader validates the 4-byte MPEG header at the start of buf and
// returns the frame's byte length and playback duration. ok=false for anything
// that is not a Layer III frame with a defined bitrate and sample rate.
func parseMP3FrameHeader(buf []byte) (frameSize int, seconds float64, ok bool) {
	if len(buf) < 4 || buf[0] != 0xFF || buf[1]&0xE0 != 0xE0 {
		return 0, 0, false
	}
	version := (buf[1] >> 3) & 0x3 // 3=MPEG-1, 2=MPEG-2, 0=MPEG-2.5, 1=reserved
	layer := (buf[1] >> 1) & 0x3   // 1=Layer III
	bitrateIdx := (buf[2] >> 4) & 0xF
	sampleRateIdx := (buf[2] >> 2) & 0x3
	padding := int((buf[2] >> 1) & 0x1)

	rates, versionOK := mp3SampleRates[version]
	if !versionOK || layer != 1 || bitrateIdx == 0 || bitrateIdx == 0xF || sampleRateIdx == 3 {
		return 0, 0, false // reserved fields, or "free" bitrate we cannot size
	}

	group := 0 // MPEG-1
	if version != 3 {
		group = 1 // MPEG-2/2.5
	}
	bitrate := mp3BitrateKbps[group][bitrateIdx] * 1000
	sampleRate := rates[sampleRateIdx]
	samples := mp3SamplesPerFrame[group]

	frameSize = samples/8*bitrate/sampleRate + padding
	if frameSize <= 4 {
		return 0, 0, false
	}
	return frameSize, float64(samples) / float64(sampleRate), true
}

// skipID3v2 returns the offset past a leading ID3v2 tag, whose size is encoded
// as a 28-bit sync-safe integer, or 0 when no tag is present.
func skipID3v2(data []byte) int {
	if len(data) < 10 || string(data[0:3]) != "ID3" {
		return 0
	}
	size := int(data[6]&0x7F)<<21 | int(data[7]&0x7F)<<14 | int(data[8]&0x7F)<<7 | int(data[9]&0x7F)
	end := 10 + size
	if end > len(data) {
		return len(data)
	}
	return end
}

// wavDurationSeconds parses a canonical RIFF/WAVE container and derives its
// duration from the format byte rate and the data chunk size. It tolerates
// extra chunks (LIST/fact/etc.) and a data chunk whose declared size is missing
// or overruns the buffer (some streamed encoders write 0 or 0xFFFFFFFF), falling
// back to the trailing byte count in that case.
func wavDurationSeconds(data []byte) (float64, bool) {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return 0, false
	}

	var byteRate uint32
	var dataSize int
	var haveFmt, haveData bool

	pos := 12
	for pos+8 <= len(data) {
		id := string(data[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		body := pos + 8

		switch id {
		case "fmt ":
			// byte rate lives at offset 8 within the fmt chunk body.
			if body+12 <= len(data) {
				byteRate = binary.LittleEndian.Uint32(data[body+8 : body+12])
				haveFmt = true
			}
		case "data":
			remaining := len(data) - body
			if size <= 0 || size > remaining {
				size = remaining
			}
			dataSize = size
			haveData = true
		}

		if haveFmt && haveData {
			break
		}
		// Advance past this chunk: an 8-byte header plus its word-aligned body.
		// pos always grows by at least the header, so the walk terminates; a
		// zero-length non-data chunk (valid) simply advances to the next header.
		// size is read from a uint32, so it is never negative.
		pos = body + size
		if size%2 == 1 {
			pos++ // chunks are word-aligned
		}
	}

	if !haveFmt || !haveData || byteRate == 0 || dataSize <= 0 {
		return 0, false
	}
	return float64(dataSize) / float64(byteRate), true
}

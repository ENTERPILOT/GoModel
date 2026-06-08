package usage

import (
	"encoding/binary"
	"strings"
)

// pcmBytesPerSecond is the byte rate of OpenAI's headerless PCM speech output:
// signed 16-bit, little-endian, mono, 24 kHz (24000 samples * 2 bytes).
const pcmBytesPerSecond = 24000 * 2

// measureSpeechDurationSeconds returns the playback duration, in seconds, of
// synthesized speech the gateway can compute without decoding a compressed
// codec. It parses self-describing WAV (RIFF/WAVE) containers and the fixed-rate
// PCM stream OpenAI emits for response_format=pcm. Compressed formats (mp3,
// opus, aac, flac) return ok=false so the caller can flag the cost as partial
// rather than reporting a silent zero.
func measureSpeechDurationSeconds(data []byte, format string) (float64, bool) {
	if seconds, ok := wavDurationSeconds(data); ok {
		return seconds, true
	}
	if normalizeAudioFormat(format) == "pcm" && len(data) > 0 {
		return float64(len(data)) / pcmBytesPerSecond, true
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

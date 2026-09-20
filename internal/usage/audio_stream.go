package usage

import (
	"bytes"
	"encoding/json"
)

// speechHeaderCapture bounds the leading bytes a SpeechDurationMeter keeps. A
// RIFF/WAVE header reaches its data chunk well inside this, including the extra
// chunks some encoders write first, and nothing past that chunk is needed: the
// duration follows from the byte rate and the byte count.
const speechHeaderCapture = 8 * 1024

// SpeechDurationMeter measures the playback duration of synthesized speech as
// it streams past, so a relayed response is priced by the same rules as a
// buffered one without holding the audio in memory. Buffering it to measure it
// would reintroduce the cost the relay exists to avoid, and capping that buffer
// would silently stop billing the duration of long speech.
//
// The three measurable formats need different things and none needs the body:
// WAV and headerless PCM follow from the header and the total byte count, and
// mp3 from the running sum of its frame headers.
type SpeechDurationMeter struct {
	format string
	head   []byte
	total  int64
	mp3    mp3FrameWalker
}

// NewSpeechDurationMeter returns a meter for speech in the given
// response_format or MIME type.
func NewSpeechDurationMeter(format string) *SpeechDurationMeter {
	return &SpeechDurationMeter{format: normalizeAudioFormat(format)}
}

// Write consumes the next piece of the audio stream. It never fails, so it can
// tee a relay without being able to break it.
func (m *SpeechDurationMeter) Write(p []byte) (int, error) {
	if m == nil {
		return len(p), nil
	}
	if room := speechHeaderCapture - len(m.head); room > 0 {
		m.head = append(m.head, p[:min(room, len(p))]...)
	}
	m.total += int64(len(p))
	if m.format == "mp3" {
		m.mp3.write(p)
	}
	return len(p), nil
}

// Seconds returns the duration of the audio written so far, and whether the
// gateway could compute it. A format it cannot measure without decoding
// (opus, aac, flac) reports false so the caller records a cost caveat rather
// than a silent zero, exactly as the buffered path does.
func (m *SpeechDurationMeter) Seconds() (float64, bool) {
	if m == nil || m.total == 0 {
		return 0, false
	}
	// A WAVE container describes itself, so it wins over the requested format
	// for the same reason it does when the whole body is in hand.
	if byteRate, dataOffset, declaredSize, ok := wavHeader(m.head); ok {
		streamed := m.total - int64(dataOffset)
		if declaredSize > 0 && int64(declaredSize) <= streamed {
			streamed = int64(declaredSize)
		}
		if streamed > 0 {
			return float64(streamed) / float64(byteRate), true
		}
		return 0, false
	}
	switch m.format {
	case "pcm":
		return float64(m.total) / pcmBytesPerSecond, true
	case "mp3":
		return m.mp3.duration()
	}
	return 0, false
}

// mp3FrameWalker sums MPEG Layer III frame durations across a stream arriving in
// arbitrarily sized pieces, matching mp3DurationSeconds frame for frame. What it
// holds is bounded by one frame — the largest Layer III frame is 1441 bytes —
// because every byte it examines is either counted into a frame or discarded, so
// the length of the audio does not bound how much of it must be kept.
type mp3FrameWalker struct {
	carry      []byte
	seconds    float64
	frames     int
	tagBytes   int  // bytes of a leading ID3v2 tag still to discard
	tagChecked bool // the leading tag, if any, has been sized
	done       bool // the audio stream ended: trailing junk, or no frames at all
}

func (w *mp3FrameWalker) write(p []byte) {
	if w.done {
		return
	}
	w.carry = append(w.carry, p...)
	w.walk()
}

func (w *mp3FrameWalker) walk() {
	if !w.tagChecked && len(w.carry) >= 10 {
		w.tagChecked = true
		w.tagBytes = id3v2TagSize(w.carry)
	}
	if w.tagBytes > 0 {
		skipped := min(w.tagBytes, len(w.carry))
		w.carry = w.carry[skipped:]
		w.tagBytes -= skipped
		if w.tagBytes > 0 {
			return // the rest of the tag is still arriving
		}
	}

	pos := 0
	for pos+4 <= len(w.carry) {
		frameSize, frameSeconds, ok := parseMP3FrameHeader(w.carry[pos:])
		if !ok {
			if w.frames > 0 {
				w.done = true // trailing tag or junk after the audio stream
				break
			}
			pos++ // still hunting for the first sync word
			continue
		}
		if pos+frameSize > len(w.carry) {
			break // the frame is still arriving; do not bill audio that is not there
		}
		w.seconds += frameSeconds
		w.frames++
		pos += frameSize
	}
	if w.done {
		w.carry = nil
		return
	}
	w.carry = w.carry[pos:]
}

func (w *mp3FrameWalker) duration() (float64, bool) {
	return w.seconds, w.frames > 0
}

// TranscriptUsageCollector picks the billable numbers out of a relayed
// transcript as it streams past, so a streamed transcription is priced from the
// provider's own usage however long the transcript runs.
//
// A transcript relayed as server-sent events reports its usage in the terminal
// transcript.text.done event, so only the newest usage-bearing event is kept and
// the transcript itself is discarded. A provider that ignored stream=true
// answers with one JSON object instead, which is the usage body itself and is
// buffered up to maxBodyBytes.
type TranscriptUsageCollector struct {
	maxBodyBytes int

	shaped bool // the response shape has been determined
	isJSON bool

	body     bytes.Buffer
	overflow bool

	line         bytes.Buffer
	lineOverflow bool
	event        []byte
}

// NewTranscriptUsageCollector returns a collector that buffers at most
// maxBodyBytes of a non-event response.
func NewTranscriptUsageCollector(maxBodyBytes int) *TranscriptUsageCollector {
	return &TranscriptUsageCollector{maxBodyBytes: maxBodyBytes}
}

// Write consumes the next piece of the transcript. It never fails, so it can tee
// a relay without being able to break it.
func (c *TranscriptUsageCollector) Write(p []byte) (int, error) {
	if c == nil {
		return len(p), nil
	}
	if !c.shaped {
		if trimmed := bytes.TrimLeft(p, " \t\r\n"); len(trimmed) > 0 {
			c.shaped = true
			c.isJSON = trimmed[0] == '{'
		}
	}
	if c.isJSON {
		c.writeBody(p)
		return len(p), nil
	}
	c.scanEvents(p)
	return len(p), nil
}

func (c *TranscriptUsageCollector) writeBody(p []byte) {
	if c.overflow {
		return
	}
	if c.body.Len()+len(p) > c.maxBodyBytes {
		c.overflow = true
		c.body.Reset()
		return
	}
	c.body.Write(p)
}

// scanEvents keeps the newest data: payload that carries a usage member,
// assembling event lines across the arbitrary boundaries the pieces arrive on.
// A single line is held to the same maxBodyBytes budget as a JSON body, so a
// stream that never emits a newline cannot grow the collector without bound;
// once a line passes it, the rest of that line is discarded.
func (c *TranscriptUsageCollector) scanEvents(p []byte) {
	for len(p) > 0 {
		newline := bytes.IndexByte(p, '\n')
		if newline < 0 {
			c.writeLine(p)
			return
		}
		c.writeLine(p[:newline])
		if !c.lineOverflow {
			c.takeUsageEvent(c.line.Bytes())
		}
		c.line.Reset()
		c.lineOverflow = false
		p = p[newline+1:]
	}
}

func (c *TranscriptUsageCollector) writeLine(p []byte) {
	if c.lineOverflow {
		return
	}
	if c.line.Len()+len(p) > c.maxBodyBytes {
		c.lineOverflow = true
		c.line.Reset()
		return
	}
	c.line.Write(p)
}

func (c *TranscriptUsageCollector) takeUsageEvent(line []byte) {
	payload := bytes.TrimSpace(line)
	if !bytes.HasPrefix(payload, sseDataPrefix) {
		return
	}
	payload = bytes.TrimSpace(payload[len(sseDataPrefix):])
	if len(payload) == 0 || payload[0] != '{' {
		return
	}
	var event struct {
		Usage json.RawMessage `json:"usage"`
	}
	// Later events win: the transcript closes with the usage-bearing one.
	if json.Unmarshal(payload, &event) == nil && len(event.Usage) > 0 {
		c.event = bytes.Clone(payload)
	}
}

// UsageBody returns the JSON object the transcription and translation
// extractors read reported usage from, or nil when the relay carried none — in
// which case the call is priced from the uploaded audio's duration, as it is for
// any provider that reports no usage.
func (c *TranscriptUsageCollector) UsageBody() []byte {
	if c == nil {
		return nil
	}
	// A final event without its newline is still a complete payload.
	if c.line.Len() > 0 && !c.lineOverflow {
		c.takeUsageEvent(c.line.Bytes())
		c.line.Reset()
	}
	if c.event != nil {
		return c.event
	}
	if c.isJSON && !c.overflow {
		return c.body.Bytes()
	}
	return nil
}

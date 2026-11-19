package longspeech

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

const (
	// unknownSize is what a WAV header declares when its writer does not know
	// the length yet. Players read such a file to its end, which is what a
	// streamed response needs: the total is not known until the last part.
	unknownSize = 0xFFFFFFFF

	// pcmFrameBytes is the sample size of headerless PCM speech as OpenAI
	// defines it: signed 16-bit mono.
	pcmFrameBytes = 2

	// maxWAVPreamble bounds the chunks read ahead of a part's audio (fmt,
	// LIST, JUNK), so a malformed header cannot make the gateway skip a body.
	maxWAVPreamble = 1 << 20
)

// Synthesizer produces the audio for one part of the text.
type Synthesizer func(ctx context.Context, text string) (*core.AudioResponse, error)

// Joinable reports whether speech in the given response_format can be joined
// without decoding it. Only uncompressed audio can: wav once the headers of
// the later parts are dropped, and pcm as it is. The compressed formats carry
// per-stream headers, encoder delay or page structure that splicing breaks.
func Joinable(responseFormat string) bool {
	switch strings.ToLower(strings.TrimSpace(responseFormat)) {
	case "wav", "pcm":
		return true
	}
	return false
}

// Join synthesizes parts one after another and returns their audio as one
// response: a single WAV file when the provider answers with WAV, the samples
// back to back when it answers with headerless PCM.
//
// The first part is synthesized before Join returns, so a request the provider
// rejects fails as an ordinary error. Each later part is requested once the
// one before it has been read, and a failure there ends the stream with that
// error. provider names the upstream in errors about the audio it returned.
func Join(ctx context.Context, provider string, parts []string, synthesize Synthesizer) (*core.AudioResponse, error) {
	stream := &joinedAudio{ctx: ctx, provider: provider, parts: parts, total: len(parts), synthesize: synthesize}
	contentType, err := stream.openNext()
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	return &core.AudioResponse{ContentType: contentType, Stream: stream}, nil
}

// joinedAudio reads the audio of every part as one stream.
type joinedAudio struct {
	ctx        context.Context
	provider   string
	synthesize Synthesizer
	parts      []string // not synthesized yet
	total      int

	wav        bool
	format     []byte // the first part's WAV fmt chunk; every part must match it
	frameBytes int    // bytes per sample frame

	pending   []byte    // bytes owed to the reader before the current part's audio
	audio     io.Reader // the current part's samples
	body      io.Closer // the current part's upstream body
	partBytes int64
	err       error
}

func (j *joinedAudio) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for j.err == nil {
		if len(j.pending) > 0 {
			n := copy(p, j.pending)
			j.pending = j.pending[n:]
			return n, nil
		}
		if j.audio == nil {
			if len(j.parts) == 0 {
				j.err = io.EOF
				break
			}
			_, j.err = j.openNext()
			continue
		}
		n, err := j.audio.Read(p)
		j.partBytes += int64(n)
		if err == io.EOF {
			j.finishPart()
		} else if err != nil {
			j.err = err
		}
		if n > 0 {
			return n, nil
		}
	}
	return 0, j.err
}

// Close releases the part being read and abandons the ones not requested yet.
func (j *joinedAudio) Close() error {
	j.parts, j.audio, j.pending = nil, nil, nil
	if j.err == nil {
		j.err = io.ErrClosedPipe
	}
	return j.closeBody()
}

func (j *joinedAudio) closeBody() error {
	if j.body == nil {
		return nil
	}
	body := j.body
	j.body = nil
	return body.Close()
}

// finishPart closes the part that just ended. A part that stopped inside a
// sample frame is completed with silence: every later sample would otherwise
// be read from the wrong byte, which turns the rest of the audio into noise.
func (j *joinedAudio) finishPart() {
	_ = j.closeBody()
	j.audio = nil
	if rest := j.partBytes % int64(j.frameBytes); rest != 0 {
		j.pending = make([]byte, int64(j.frameBytes)-rest)
	}
	j.partBytes = 0
}

// openNext requests the next part and positions the stream at its samples. It
// returns the content type the provider gave the audio.
func (j *joinedAudio) openNext() (string, error) {
	number := j.total - len(j.parts) + 1
	text := j.parts[0]
	j.parts = j.parts[1:]

	resp, err := j.synthesize(j.ctx, text)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", core.NewEmptyProviderResponseError(j.provider)
	}
	body := resp.Stream
	if body == nil {
		body = io.NopCloser(bytes.NewReader(resp.Data))
	}
	j.body = body

	in := bufio.NewReader(body)
	head, err := in.Peek(12)
	if len(head) == 0 {
		if errors.Is(err, io.EOF) {
			err = nil // an empty body, not a failed read
		}
		return "", j.partError(number, "no audio", err)
	}
	if number == 1 {
		j.wav = isWAV(head)
		j.frameBytes = pcmFrameBytes
		if !j.wav && !rawPCM(resp.ContentType, head) {
			return "", j.partError(number, "audio that is neither WAV nor PCM", nil)
		}
	}
	j.audio = in
	if !j.wav {
		return contentTypeOr(resp.ContentType, "pcm"), nil
	}

	format, dataSize, err := readWAVHeader(in)
	if err == nil && binary.LittleEndian.Uint16(format[12:14]) == 0 {
		err = errMalformedWAV // no block size to keep the parts aligned by
	}
	if err != nil {
		return "", j.partError(number, "a WAV header that cannot be read", err)
	}
	if number == 1 {
		j.format = format
		j.frameBytes = int(binary.LittleEndian.Uint16(format[12:14]))
		j.pending = streamedWAVHeader(format)
	} else if !bytes.Equal(format, j.format) {
		return "", j.partError(number, "audio in a different format than the first part", nil)
	}
	// A declared size bounds the samples, so chunks written after them (LIST
	// metadata) do not end up inside the audio. A streamed part declares none.
	if dataSize != 0 && dataSize != unknownSize {
		j.audio = io.LimitReader(in, int64(dataSize))
	}
	return contentTypeOr(resp.ContentType, "wav"), nil
}

func (j *joinedAudio) partError(number int, returned string, cause error) error {
	return core.NewProviderError(j.provider, http.StatusBadGateway,
		fmt.Sprintf("provider returned %s for part %d of %d of the split speech input", returned, number, j.total), cause)
}

func contentTypeOr(contentType, format string) string {
	if contentType = strings.TrimSpace(contentType); contentType != "" {
		return contentType
	}
	return core.SpeechResponseContentType(format)
}

func isWAV(head []byte) bool {
	return len(head) >= 12 && string(head[0:4]) == "RIFF" && string(head[8:12]) == "WAVE"
}

// rawPCM reports whether a body that is not WAV can be taken as headerless
// samples. Nothing in such a body says so, so this only rules out what is
// recognizably something else: a compressed stream or a non-audio payload.
func rawPCM(contentType string, head []byte) bool {
	for _, magic := range []string{"ID3", "OggS", "fLaC"} {
		if bytes.HasPrefix(head, []byte(magic)) {
			return false
		}
	}
	mediaType, _, _ := strings.Cut(contentType, ";")
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	switch mediaType {
	case "", "application/octet-stream":
		return true
	case "audio/mpeg", "audio/mp3", "audio/ogg", "audio/opus", "audio/aac", "audio/flac", "audio/x-flac", "audio/mp4", "audio/webm":
		return false
	}
	return strings.HasPrefix(mediaType, "audio/")
}

// errMalformedWAV reports a RIFF/WAVE header that does not lead to samples.
var errMalformedWAV = errors.New("malformed WAV header")

// readWAVHeader consumes a RIFF/WAVE header up to the first sample and returns
// the fmt chunk body and the size the data chunk declares.
func readWAVHeader(in io.Reader) ([]byte, uint32, error) {
	var riff [12]byte
	if _, err := io.ReadFull(in, riff[:]); err != nil {
		return nil, 0, err
	}
	if !isWAV(riff[:]) {
		return nil, 0, errMalformedWAV
	}
	var (
		format   []byte
		preamble int64
	)
	for {
		var chunk [8]byte
		if _, err := io.ReadFull(in, chunk[:]); err != nil {
			return nil, 0, err
		}
		id, size := string(chunk[:4]), binary.LittleEndian.Uint32(chunk[4:])
		if id == "data" {
			if format == nil {
				return nil, 0, errMalformedWAV
			}
			return format, size, nil
		}
		padded := int64(size) + int64(size&1) // chunks are word-aligned
		if preamble += 8 + padded; preamble > maxWAVPreamble {
			return nil, 0, errMalformedWAV
		}
		if id != "fmt " {
			if _, err := io.CopyN(io.Discard, in, padded); err != nil {
				return nil, 0, err
			}
			continue
		}
		if size < 16 {
			return nil, 0, errMalformedWAV
		}
		body := make([]byte, padded)
		if _, err := io.ReadFull(in, body); err != nil {
			return nil, 0, err
		}
		format = body[:size]
	}
}

// streamedWAVHeader builds the header of the joined file: the first part's
// format, and sizes that say the length is not known yet. The first part's
// own header cannot be reused, because it declares that part's length and a
// player would stop there.
func streamedWAVHeader(format []byte) []byte {
	header := make([]byte, 0, 28+len(format)+1)
	header = append(header, "RIFF"...)
	header = binary.LittleEndian.AppendUint32(header, unknownSize)
	header = append(header, "WAVEfmt "...)
	header = binary.LittleEndian.AppendUint32(header, uint32(len(format))) //nolint:gosec // G115: readWAVHeader caps a fmt chunk at maxWAVPreamble.
	header = append(header, format...)
	if len(format)%2 == 1 {
		header = append(header, 0)
	}
	header = append(header, "data"...)
	return binary.LittleEndian.AppendUint32(header, unknownSize)
}

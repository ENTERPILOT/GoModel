package longspeech

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// wavFile describes a WAV body the way a provider might write it.
type wavFile struct {
	channels   uint16
	sampleRate uint32
	samples    []byte
	declared   *uint32 // data size the header claims; nil means the true size
	before     []byte  // a LIST chunk body placed ahead of the samples
	after      []byte  // bytes written after the samples
}

func (f wavFile) format() []byte {
	channels := f.channels
	if channels == 0 {
		channels = 1
	}
	sampleRate := f.sampleRate
	if sampleRate == 0 {
		sampleRate = 24000
	}
	format := binary.LittleEndian.AppendUint16(nil, 1) // PCM
	format = binary.LittleEndian.AppendUint16(format, channels)
	format = binary.LittleEndian.AppendUint32(format, sampleRate)
	format = binary.LittleEndian.AppendUint32(format, sampleRate*uint32(channels)*2)
	format = binary.LittleEndian.AppendUint16(format, channels*2)
	return binary.LittleEndian.AppendUint16(format, 16)
}

func (f wavFile) bytes() []byte {
	declared := uint32(len(f.samples))
	if f.declared != nil {
		declared = *f.declared
	}
	out := []byte("RIFF")
	out = binary.LittleEndian.AppendUint32(out, 36+declared)
	out = append(out, "WAVEfmt "...)
	out = binary.LittleEndian.AppendUint32(out, 16)
	out = append(out, f.format()...)
	if f.before != nil {
		out = append(out, "LIST"...)
		out = binary.LittleEndian.AppendUint32(out, uint32(len(f.before)))
		out = append(out, f.before...)
		if len(f.before)%2 == 1 {
			out = append(out, 0)
		}
	}
	out = append(out, "data"...)
	out = binary.LittleEndian.AppendUint32(out, declared)
	out = append(out, f.samples...)
	return append(out, f.after...)
}

// joinedWAV is the file Join must produce for the given format and samples.
func joinedWAV(format []byte, samples ...[]byte) []byte {
	return append(streamedWAVHeader(format), bytes.Join(samples, nil)...)
}

// closeRecorder counts how often a part's body was closed.
type closeRecorder struct {
	io.Reader
	closes int
}

func (c *closeRecorder) Close() error {
	c.closes++
	return nil
}

// scriptedSynthesizer answers each part with the next scripted response and
// records what it was asked for.
type scriptedSynthesizer struct {
	responses []*core.AudioResponse
	errs      map[int]error // by zero-based call
	texts     []string
}

func (s *scriptedSynthesizer) synthesize(_ context.Context, text string) (*core.AudioResponse, error) {
	call := len(s.texts)
	s.texts = append(s.texts, text)
	if err := s.errs[call]; err != nil {
		return nil, err
	}
	return s.responses[call], nil
}

func buffered(contentType string, data []byte) *core.AudioResponse {
	return &core.AudioResponse{ContentType: contentType, Data: data}
}

// streamed delivers data one byte per read, the least convenient way a body
// can arrive.
func streamed(contentType string, data []byte) *core.AudioResponse {
	return &core.AudioResponse{ContentType: contentType, Stream: io.NopCloser(iotest.OneByteReader(bytes.NewReader(data)))}
}

func TestJoin_Audio(t *testing.T) {
	mono := wavFile{}.format()
	stereo := wavFile{channels: 2}.format()
	unknown := uint32(unknownSize)
	zero := uint32(0)
	overstated := uint32(0x7FFFFFFF)

	tests := []struct {
		name        string
		responses   []*core.AudioResponse
		wantType    string
		wantContent []byte
	}{
		{
			name: "wav parts become one file with a single header",
			responses: []*core.AudioResponse{
				buffered("audio/wav", wavFile{samples: []byte("aaaa")}.bytes()),
				streamed("audio/wav", wavFile{samples: []byte("bbbbbb")}.bytes()),
				buffered("audio/wav", wavFile{samples: []byte("cc")}.bytes()),
			},
			wantType:    "audio/wav",
			wantContent: joinedWAV(mono, []byte("aaaa"), []byte("bbbbbb"), []byte("cc")),
		},
		{
			name: "chunks around the samples are dropped",
			responses: []*core.AudioResponse{
				streamed("audio/wav", wavFile{samples: []byte("aaaa"), before: []byte("INFOodd"), after: []byte("LIST\x04\x00\x00\x00junk")}.bytes()),
				buffered("audio/wav", wavFile{samples: []byte("bbbb"), after: []byte("LIST\x04\x00\x00\x00junk")}.bytes()),
			},
			wantType:    "audio/wav",
			wantContent: joinedWAV(mono, []byte("aaaa"), []byte("bbbb")),
		},
		{
			name: "a part that declares no length is read to its end",
			responses: []*core.AudioResponse{
				streamed("audio/wav", wavFile{samples: []byte("aaaa"), declared: &unknown}.bytes()),
				streamed("audio/wav", wavFile{samples: []byte("bbbb"), declared: &zero}.bytes()),
			},
			wantType:    "audio/wav",
			wantContent: joinedWAV(mono, []byte("aaaa"), []byte("bbbb")),
		},
		{
			// A clean end before the declared size is a header that overstates
			// (encoders that stream write placeholders), not a cut transfer:
			// that one fails the read instead.
			name: "a part that declares more than it carries is read to its end",
			responses: []*core.AudioResponse{
				streamed("audio/wav", wavFile{samples: []byte("aaaa"), declared: &overstated}.bytes()),
				buffered("audio/wav", wavFile{samples: []byte("bbbb")}.bytes()),
			},
			wantType:    "audio/wav",
			wantContent: joinedWAV(mono, []byte("aaaa"), []byte("bbbb")),
		},
		{
			name: "a wav part cut inside a frame is completed with silence",
			responses: []*core.AudioResponse{
				streamed("audio/wav", wavFile{channels: 2, samples: []byte("aaaaa"), declared: &unknown}.bytes()),
				buffered("audio/wav", wavFile{channels: 2, samples: []byte("bbbb")}.bytes()),
			},
			wantType:    "audio/wav",
			wantContent: joinedWAV(stereo, []byte("aaaaa\x00\x00\x00"), []byte("bbbb")),
		},
		{
			name: "pcm parts are played back to back",
			responses: []*core.AudioResponse{
				streamed("audio/pcm", []byte("aaaa")),
				buffered("audio/pcm", []byte("bb")),
			},
			wantType:    "audio/pcm",
			wantContent: []byte("aaaabb"),
		},
		{
			name: "a pcm part with half a sample is completed with silence",
			responses: []*core.AudioResponse{
				streamed("audio/pcm", []byte("aaa")),
				buffered("audio/pcm", []byte("bb")),
			},
			wantType:    "audio/pcm",
			wantContent: []byte("aaa\x00bb"),
		},
		{
			name: "a provider that answers pcm with wav is joined as wav",
			responses: []*core.AudioResponse{
				buffered("", wavFile{samples: []byte("aaaa")}.bytes()),
				buffered("", wavFile{samples: []byte("bbbb")}.bytes()),
			},
			wantType:    "audio/wav",
			wantContent: joinedWAV(mono, []byte("aaaa"), []byte("bbbb")),
		},
		{
			name: "headerless audio without a content type is pcm",
			responses: []*core.AudioResponse{
				buffered("", []byte("aaaa")),
				buffered("application/octet-stream", []byte("bbbb")),
			},
			wantType:    "audio/pcm",
			wantContent: []byte("aaaabbbb"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synth := &scriptedSynthesizer{responses: tt.responses}
			parts := make([]string, len(tt.responses))
			for i := range parts {
				parts[i] = string(rune('A' + i))
			}

			resp, err := Join(context.Background(), "acme", parts, synth.synthesize)
			require.NoError(t, err)
			assert.Equal(t, tt.wantType, resp.ContentType)
			// Only the first part is requested up front; the rest wait their turn.
			assert.Equal(t, parts[:1], synth.texts)

			got, err := io.ReadAll(resp.Stream)
			require.NoError(t, err)
			assert.Equal(t, tt.wantContent, got)
			assert.Equal(t, parts, synth.texts)
			require.NoError(t, resp.Stream.Close())
		})
	}
}

func TestJoin_FirstPartFailsBeforeAnyAudio(t *testing.T) {
	rejected := core.NewInvalidRequestError("voice not found", nil)
	tests := []struct {
		name     string
		response *core.AudioResponse
		err      error
		want     string
		cause    string
	}{
		{name: "provider error is returned as it is", err: rejected, want: "voice not found"},
		{name: "no response", want: "empty response"},
		{name: "empty audio", response: buffered("audio/wav", nil), want: "no audio for part 1 of 2"},
		{
			name:     "body that cannot be read",
			response: &core.AudioResponse{ContentType: "audio/wav", Stream: io.NopCloser(iotest.ErrReader(errors.New("connection reset")))},
			want:     "no audio for part 1 of 2",
			cause:    "connection reset",
		},
		{name: "mp3 by content type", response: buffered("audio/mpeg", []byte("\xff\xfb\x90\x00....")), want: "neither WAV nor PCM"},
		{name: "mp3 by tag", response: buffered("audio/pcm", []byte("ID3\x04\x00\x00\x00\x00\x00\x00")), want: "neither WAV nor PCM"},
		{name: "not audio", response: buffered("application/json", []byte(`{"error":"nope"}`)), want: "neither WAV nor PCM"},
		{name: "labelled wav without a riff header", response: buffered("audio/wav", []byte("bbbbbbbbbbbbbbbb")), want: "neither WAV nor PCM"},
		{name: "wav without samples", response: buffered("audio/wav", wavFile{}.bytes()[:36]), want: "WAV header that cannot be read"},
		{name: "wav without a block size", response: buffered("audio/wav", zeroBlockAlign(wavFile{samples: []byte("aaaa")}.bytes())), want: "WAV header that cannot be read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synth := &scriptedSynthesizer{responses: []*core.AudioResponse{tt.response}, errs: map[int]error{0: tt.err}}

			resp, err := Join(context.Background(), "acme", []string{"A", "B"}, synth.synthesize)
			require.Nil(t, resp)
			require.ErrorContains(t, err, tt.want)
			assert.Len(t, synth.texts, 1, "no further part may be requested")
			if tt.err != nil {
				assert.Same(t, tt.err, err)
				return
			}
			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, http.StatusBadGateway, gatewayErr.StatusCode)
			assert.Equal(t, "acme", gatewayErr.Provider)
			if tt.cause != "" {
				require.ErrorContains(t, gatewayErr.Err, tt.cause)
			}
		})
	}
}

func zeroBlockAlign(wav []byte) []byte {
	wav[32], wav[33] = 0, 0
	return wav
}

func TestJoin_LaterPartFailureEndsTheStream(t *testing.T) {
	upstream := errors.New("upstream went away")
	tests := []struct {
		name     string
		second   *core.AudioResponse
		err      error
		want     string
		wantCode int
	}{
		{name: "provider error", err: upstream, want: "upstream went away"},
		{
			name:     "different sample rate",
			second:   buffered("audio/wav", wavFile{sampleRate: 44100, samples: []byte("bbbb")}.bytes()),
			want:     "different format than the first part for part 2 of 3",
			wantCode: http.StatusBadGateway,
		},
		{
			name:     "not wav",
			second:   buffered("audio/wav", []byte("bbbbbbbbbbbbbbbb")),
			want:     "different format than the first part for part 2 of 3",
			wantCode: http.StatusBadGateway,
		},
		{
			name:     "wav with a broken header",
			second:   buffered("audio/wav", wavFile{}.bytes()[:36]),
			want:     "WAV header that cannot be read",
			wantCode: http.StatusBadGateway,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synth := &scriptedSynthesizer{
				responses: []*core.AudioResponse{buffered("audio/wav", wavFile{samples: []byte("aaaa")}.bytes()), tt.second},
				errs:      map[int]error{1: tt.err},
			}
			resp, err := Join(context.Background(), "acme", []string{"A", "B", "C"}, synth.synthesize)
			require.NoError(t, err)

			got, err := io.ReadAll(resp.Stream)
			require.ErrorContains(t, err, tt.want)
			// What was synthesized before the failure still reached the reader.
			assert.Equal(t, joinedWAV(wavFile{}.format(), []byte("aaaa")), got)
			if tt.wantCode != 0 {
				var gatewayErr *core.GatewayError
				require.ErrorAs(t, err, &gatewayErr)
				assert.Equal(t, tt.wantCode, gatewayErr.StatusCode)
			}

			// The failure is final: the third part is never requested.
			_, err = resp.Stream.Read(make([]byte, 8))
			require.ErrorContains(t, err, tt.want)
			assert.Len(t, synth.texts, 2)
		})
	}
}

// TestJoin_LaterPCMPartMustBePCM: headerless audio carries nothing that says
// what it is, so every part is checked for what would otherwise be played as
// samples.
func TestJoin_LaterPCMPartMustBePCM(t *testing.T) {
	tests := []struct {
		name   string
		second *core.AudioResponse
		want   string
	}{
		{name: "wav", second: buffered("audio/wav", wavFile{samples: []byte("bbbb")}.bytes()), want: "different format than the first part"},
		{name: "mp3", second: buffered("audio/mpeg", []byte("\xff\xfb\x90\x00....")), want: "neither WAV nor PCM"},
		{name: "error body", second: buffered("application/json", []byte(`{"error":"nope"}`)), want: "neither WAV nor PCM"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synth := &scriptedSynthesizer{responses: []*core.AudioResponse{buffered("audio/pcm", []byte("aaaa")), tt.second}}
			resp, err := Join(context.Background(), "acme", []string{"A", "B"}, synth.synthesize)
			require.NoError(t, err)

			got, err := io.ReadAll(resp.Stream)
			require.ErrorContains(t, err, tt.want)
			assert.Equal(t, "aaaa", string(got))
		})
	}
}

// blockingBody blocks its reader until it is closed, as a provider body does
// while the model is still synthesizing.
type blockingBody struct {
	reading chan struct{}
	closed  chan struct{}
}

func (b *blockingBody) Read([]byte) (int, error) {
	close(b.reading)
	<-b.closed
	return 0, errors.New("body closed")
}

func (b *blockingBody) Close() error {
	close(b.closed)
	return nil
}

// TestJoin_CloseUnblocksARead: a relay that gives up (the client left) closes
// the stream from another goroutine while a read is waiting on the provider.
func TestJoin_CloseUnblocksARead(t *testing.T) {
	second := &blockingBody{reading: make(chan struct{}), closed: make(chan struct{})}
	synth := &scriptedSynthesizer{responses: []*core.AudioResponse{
		buffered("audio/pcm", []byte("aaaa")),
		{ContentType: "audio/pcm", Stream: second},
	}}
	resp, err := Join(context.Background(), "acme", []string{"A", "B", "C"}, synth.synthesize)
	require.NoError(t, err)

	read := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(resp.Stream)
		read <- err
	}()
	<-second.reading
	require.NoError(t, resp.Stream.Close())

	require.Error(t, <-read)
	assert.Len(t, synth.texts, 2, "no part is requested once the stream is closed")
}

// TestJoin_CloseCancelsAPartBeingRequested: closing while the next part is
// still being requested cancels that request, and a body that arrives anyway
// is released.
func TestJoin_CloseCancelsAPartBeingRequested(t *testing.T) {
	requesting := make(chan struct{})
	late := &closeRecorder{Reader: bytes.NewReader([]byte("bbbb"))}
	calls := 0
	synthesize := func(ctx context.Context, _ string) (*core.AudioResponse, error) {
		if calls++; calls == 1 {
			return buffered("audio/pcm", []byte("aaaa")), nil
		}
		close(requesting)
		<-ctx.Done()
		return &core.AudioResponse{ContentType: "audio/pcm", Stream: late}, nil
	}
	resp, err := Join(context.Background(), "acme", []string{"A", "B"}, synthesize)
	require.NoError(t, err)

	read := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(resp.Stream)
		read <- err
	}()
	<-requesting
	require.NoError(t, resp.Stream.Close())

	require.ErrorIs(t, <-read, io.ErrClosedPipe)
	assert.Equal(t, 1, late.closes)
}

func TestJoin_CloseStopsRequestingParts(t *testing.T) {
	body := &closeRecorder{Reader: bytes.NewReader([]byte("aaaa"))}
	synth := &scriptedSynthesizer{responses: []*core.AudioResponse{{ContentType: "audio/pcm", Stream: body}}}

	resp, err := Join(context.Background(), "acme", []string{"A", "B"}, synth.synthesize)
	require.NoError(t, err)
	require.NoError(t, resp.Stream.Close())

	assert.Equal(t, 1, body.closes)
	_, err = resp.Stream.Read(make([]byte, 8))
	require.Error(t, err)
	assert.Len(t, synth.texts, 1)
}

func TestJoin_ClosesEachPartOnceRead(t *testing.T) {
	first := &closeRecorder{Reader: bytes.NewReader([]byte("aaaa"))}
	second := &closeRecorder{Reader: bytes.NewReader([]byte("bbbb"))}
	synth := &scriptedSynthesizer{responses: []*core.AudioResponse{
		{ContentType: "audio/pcm", Stream: first},
		{ContentType: "audio/pcm", Stream: second},
	}}

	resp, err := Join(context.Background(), "acme", []string{"A", "B"}, synth.synthesize)
	require.NoError(t, err)
	got, err := io.ReadAll(resp.Stream)
	require.NoError(t, err)

	require.NoError(t, resp.Stream.Close())

	assert.Equal(t, "aaaabbbb", string(got))
	assert.Equal(t, 1, first.closes)
	assert.Equal(t, 1, second.closes)
}

func TestJoinable(t *testing.T) {
	for format, want := range map[string]bool{
		"wav": true, "pcm": true, " WAV ": true,
		"": false, "mp3": false, "opus": false, "aac": false, "flac": false,
	} {
		assert.Equal(t, want, Joinable(format), "format %q", format)
	}
}

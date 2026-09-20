package server

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/usage"
)

// transcriptSSE is a stream=true transcript: two deltas and the terminal done
// event that carries the provider-reported usage.
var transcriptSSE = [][]byte{
	[]byte("data: {\"type\":\"transcript.text.delta\",\"delta\":\"Hello\"}\n\n"),
	[]byte("data: {\"type\":\"transcript.text.delta\",\"delta\":\" there\"}\n\n"),
	[]byte("data: {\"type\":\"transcript.text.done\",\"text\":\"Hello there\"," +
		"\"usage\":{\"type\":\"tokens\",\"input_tokens\":7,\"output_tokens\":3,\"total_tokens\":10}}\n\n"),
	[]byte("data: [DONE]\n\n"),
}

// flushRecordingWriter counts the flushes a handler performs, so a test can tell
// a response that reached the client chunk by chunk from one written at once.
type flushRecordingWriter struct {
	http.ResponseWriter
	flushes int
}

func (w *flushRecordingWriter) Flush() {
	w.flushes++
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// newStreamingSpeechRequest builds a speech call whose provider relays chunks.
func newStreamingSpeechRequest(t *testing.T, flushes *flushRecordingWriter) (*echo.Context, *httptest.ResponseRecorder, *auditlog.LogEntry) {
	t.Helper()
	body := `{"model":"gpt-4o-mini-tts","input":"hello","voice":"alloy"}`
	c, rec := echotest.Post(t, "/v1/audio/speech", body,
		echotest.WithResponseWriter(func(w http.ResponseWriter) http.ResponseWriter {
			flushes.ResponseWriter = w
			return flushes
		}))
	entry := &auditlog.LogEntry{}
	c.Set(string(auditlog.LogEntryKey), entry)
	return c, rec, entry
}

func streamingSpeechMock(chunks [][]byte, contentType string) *audioMockProvider {
	return &audioMockProvider{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-mini-tts"}},
		speechResp: &core.AudioResponse{
			ContentType: contentType,
			Stream:      &chunkedReadCloser{chunks: chunks},
		},
	}
}

// TestAudioSpeech_RelaysStreamChunkByChunk pins the fix for buffered audio: a
// provider body that is still being produced reaches the client one chunk at a
// time, each flushed, instead of being held until the generation completes.
func TestAudioSpeech_RelaysStreamChunkByChunk(t *testing.T) {
	chunks := [][]byte{[]byte("first-"), []byte("second-"), []byte("third")}
	flushes := &flushRecordingWriter{}
	svc := &audioService{provider: streamingSpeechMock(chunks, "audio/mpeg")}
	c, rec, _ := newStreamingSpeechRequest(t, flushes)

	require.NoError(t, svc.CreateSpeech(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "audio/mpeg", rec.Header().Get("Content-Type"))
	assert.Equal(t, "first-second-third", rec.Body.String())
	// One flush before the first read (headers) plus one per chunk.
	assert.Equal(t, len(chunks)+1, flushes.flushes)
}

// TestAudioSpeech_StreamedResponseCosted verifies the relayed bytes still back
// output-duration pricing: usage is extracted from what the relay saw, not from
// an empty Data field.
func TestAudioSpeech_StreamedResponseCosted(t *testing.T) {
	// Half a second of 24 kHz mono 16-bit audio, split so the relay sees the
	// header and the samples as separate chunks.
	wav := wavBytes(24000, 1, 16, 0.5)
	var captured *usage.UsageEntry
	logger := &capturingUsageLogger{config: usage.Config{Enabled: true}, captured: &captured}
	svc := &audioService{
		provider:        streamingSpeechMock([][]byte{wav[:44], wav[44:]}, "audio/wav"),
		usageLogger:     logger,
		pricingResolver: &mockPricingResolver{pricing: &core.ModelPricing{PerSecondOutput: new(0.00025)}},
	}
	c, rec, _ := newStreamingSpeechRequest(t, &flushRecordingWriter{})

	require.NoError(t, svc.CreateSpeech(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, captured)
	assert.InDelta(t, 0.5, captured.RawData["audio_output_seconds"], 0.001)
	require.NotNil(t, captured.TotalCost)
	assert.InDelta(t, 0.000125, *captured.TotalCost, 1e-9)
	assert.Empty(t, captured.CostsCalculationCaveat)
}

// TestAudioSpeech_StreamedResponseAudited verifies a relayed body is still
// captured for playback in the audit entry, which only has the bytes once the
// relay finishes.
func TestAudioSpeech_StreamedResponseAudited(t *testing.T) {
	svc := &audioService{
		provider:       streamingSpeechMock([][]byte{[]byte("synthetic-"), []byte("audio")}, "audio/mpeg"),
		logBodies:      true,
		logAudioBodies: true,
	}
	c, rec, entry := newStreamingSpeechRequest(t, &flushRecordingWriter{})

	require.NoError(t, svc.CreateSpeech(c))
	require.Equal(t, http.StatusOK, rec.Code)
	respBody, ok := entry.Data.ResponseBody.(auditlog.AudioBodyLog)
	require.True(t, ok, "response body not captured as audio, got %T", entry.Data.ResponseBody)
	assert.True(t, respBody.Stored)
	assert.Equal(t, len("synthetic-audio"), respBody.Bytes)
}

// newStreamingTranscriptionRequest builds a stream=true transcription call whose
// provider answers with server-sent events.
func newStreamingTranscriptionRequest(t *testing.T, flushes *flushRecordingWriter) (*echo.Context, *httptest.ResponseRecorder, *auditlog.LogEntry) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	require.NoError(t, w.WriteField("model", "gpt-4o-transcribe"))
	require.NoError(t, w.WriteField("stream", "true"))
	part, err := w.CreateFormFile("file", "speech.mp3")
	require.NoError(t, err)
	_, err = part.Write([]byte("uploaded-audio-bytes"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	c, rec := echotest.Post(t, "/v1/audio/transcriptions", &buf,
		echotest.WithContentType(w.FormDataContentType()),
		echotest.WithResponseWriter(func(rw http.ResponseWriter) http.ResponseWriter {
			flushes.ResponseWriter = rw
			return flushes
		}))
	entry := &auditlog.LogEntry{}
	c.Set(string(auditlog.LogEntryKey), entry)
	return c, rec, entry
}

func streamingTranscriptionMock() *audioMockProvider {
	return &audioMockProvider{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-transcribe"}},
		transcriptionResp: &core.AudioResponse{
			ContentType: "text/event-stream",
			Stream:      &chunkedReadCloser{chunks: transcriptSSE},
		},
	}
}

// TestAudioTranscription_RelaysEventStream covers the reported bug directly: a
// stream=true transcript is flushed event by event with the streaming response
// headers, rather than delivered as one block at the end.
func TestAudioTranscription_RelaysEventStream(t *testing.T) {
	flushes := &flushRecordingWriter{}
	svc := &audioService{provider: streamingTranscriptionMock()}
	c, rec, _ := newStreamingTranscriptionRequest(t, flushes)

	require.NoError(t, svc.CreateTranscription(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	assert.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
	assert.Equal(t, len(transcriptSSE)+1, flushes.flushes)
	body := rec.Body.String()
	assert.Contains(t, body, `"delta":"Hello"`)
	assert.Contains(t, body, "data: [DONE]")
}

// TestAudioTranscription_StreamedUsageFromDoneEvent verifies a relayed
// transcript is priced from the provider's own token counts, carried by the
// terminal transcript.text.done event, rather than from the upload duration.
func TestAudioTranscription_StreamedUsageFromDoneEvent(t *testing.T) {
	var captured *usage.UsageEntry
	logger := &capturingUsageLogger{config: usage.Config{Enabled: true}, captured: &captured}
	svc := &audioService{provider: streamingTranscriptionMock(), usageLogger: logger}
	c, rec, _ := newStreamingTranscriptionRequest(t, &flushRecordingWriter{})

	require.NoError(t, svc.CreateTranscription(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, captured)
	assert.Equal(t, "/v1/audio/transcriptions", captured.Endpoint)
	assert.Equal(t, 7, captured.InputTokens)
	assert.Equal(t, 3, captured.OutputTokens)
	assert.Equal(t, 10, captured.TotalTokens)
}

// TestAudioTranscription_StreamedTranscriptAudited verifies the relayed events
// are recorded on the audit entry: the audit middleware skips a response that
// announces itself as a stream, so the service captures it.
func TestAudioTranscription_StreamedTranscriptAudited(t *testing.T) {
	svc := &audioService{provider: streamingTranscriptionMock(), logBodies: true}
	c, rec, entry := newStreamingTranscriptionRequest(t, &flushRecordingWriter{})

	require.NoError(t, svc.CreateTranscription(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, entry.Data)
	body, ok := entry.Data.ResponseBody.(string)
	require.True(t, ok, "streamed transcript not captured as text, got %T", entry.Data.ResponseBody)
	assert.Contains(t, body, "transcript.text.done")
}

// TestRelayAudioStream_ClosesProviderStream pins that the relay always releases
// the upstream body, so a provider connection cannot be leaked per request.
func TestRelayAudioStream_ClosesProviderStream(t *testing.T) {
	stream := &closeTrackingReadCloser{Reader: bytes.NewReader([]byte("audio"))}
	svc := &audioService{provider: &audioMockProvider{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-mini-tts"}},
		speechResp:   &core.AudioResponse{ContentType: "audio/mpeg", Stream: stream},
	}}
	c, rec, _ := newStreamingSpeechRequest(t, &flushRecordingWriter{})

	require.NoError(t, svc.CreateSpeech(c))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, stream.closed, "provider stream was not closed")
}

type closeTrackingReadCloser struct {
	io.Reader
	closed bool
}

func (r *closeTrackingReadCloser) Close() error {
	r.closed = true
	return nil
}

// TestAudioSpeech_OversizeStreamStillCosted guards the accounting seam: audit
// capture is bounded at maxCapturedAudioResponseBytes, and usage must not be
// bounded with it. A response past that ceiling is priced by the duration the
// relay measured, not written off with a cost caveat.
func TestAudioSpeech_OversizeStreamStillCosted(t *testing.T) {
	// 200 s of 24 kHz mono 16-bit audio is 9.6 MB, past the capture ceiling.
	const seconds = 200.0
	wav := wavBytes(24000, 1, 16, seconds)
	require.Greater(t, len(wav), maxCapturedAudioResponseBytes)

	chunks := make([][]byte, 0, len(wav)/(64*1024)+1)
	for start := 0; start < len(wav); start += 64 * 1024 {
		chunks = append(chunks, wav[start:min(start+64*1024, len(wav))])
	}

	var captured *usage.UsageEntry
	logger := &capturingUsageLogger{config: usage.Config{Enabled: true}, captured: &captured}
	svc := &audioService{
		provider:        streamingSpeechMock(chunks, "audio/wav"),
		usageLogger:     logger,
		pricingResolver: &mockPricingResolver{pricing: &core.ModelPricing{PerSecondOutput: new(0.00025)}},
		logBodies:       true,
		logAudioBodies:  true,
	}
	c, rec, entry := newStreamingSpeechRequest(t, &flushRecordingWriter{})

	require.NoError(t, svc.CreateSpeech(c))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, len(wav), rec.Body.Len(), "client did not receive the whole response")

	require.NotNil(t, captured)
	assert.InDelta(t, seconds, captured.RawData["audio_output_seconds"], 0.001)
	require.NotNil(t, captured.TotalCost)
	assert.InDelta(t, seconds*0.00025, *captured.TotalCost, 1e-9)
	assert.Empty(t, captured.CostsCalculationCaveat)

	// The audit body stays bounded: that ceiling is deliberate.
	respBody, ok := entry.Data.ResponseBody.(auditlog.AudioBodyLog)
	require.True(t, ok, "response body not captured as audio, got %T", entry.Data.ResponseBody)
	assert.False(t, respBody.Stored, "an oversize body was embedded in the audit entry")
}

// TestAudioTranscription_OversizeStreamStillCosted is the transcript half of the
// same guard: a transcript past the capture ceiling is still priced from the
// provider's own token counts in its terminal event.
func TestAudioTranscription_OversizeStreamStillCosted(t *testing.T) {
	filler := []byte("data: {\"type\":\"transcript.text.delta\",\"delta\":\"" +
		strings.Repeat("word ", 6000) + "\"}\n\n") // 30 kB per event
	chunks := make([][]byte, 0, 350)
	for range 350 { // ~10 MB of deltas
		chunks = append(chunks, filler)
	}
	chunks = append(chunks, transcriptSSE[2], transcriptSSE[3])

	var captured *usage.UsageEntry
	logger := &capturingUsageLogger{config: usage.Config{Enabled: true}, captured: &captured}
	svc := &audioService{
		provider: &audioMockProvider{
			mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-transcribe"}},
			transcriptionResp: &core.AudioResponse{
				ContentType: "text/event-stream",
				Stream:      &chunkedReadCloser{chunks: chunks},
			},
		},
		usageLogger: logger,
	}
	c, rec, _ := newStreamingTranscriptionRequest(t, &flushRecordingWriter{})

	require.NoError(t, svc.CreateTranscription(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Greater(t, rec.Body.Len(), maxCapturedAudioResponseBytes)

	require.NotNil(t, captured)
	assert.Equal(t, 7, captured.InputTokens)
	assert.Equal(t, 3, captured.OutputTokens)
	assert.Equal(t, 10, captured.TotalTokens)
}

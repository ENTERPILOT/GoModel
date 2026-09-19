package audiocpp

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// wavServer answers like audio.cpp's speech route: WAV bytes with the timing
// headers it attaches.
func wavServer(t testing.TB) (string, *providertest.Capture) {
	t.Helper()
	server, capture := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte("RIFF....WAVE"))
	})
	return server.URL, capture
}

// multipartFields decodes the parts of a recorded multipart body, keeping
// repeated names in order. The file part is reported separately.
func multipartFields(t testing.TB, req providertest.Recorded) (fields []core.FormField, filename string, content string) {
	t.Helper()
	_, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	require.NoError(t, err)
	reader := multipart.NewReader(strings.NewReader(string(req.Body)), params["boundary"])
	for {
		part, err := reader.NextPart()
		if err != nil {
			break
		}
		value, err := io.ReadAll(part)
		require.NoError(t, err)
		if part.FormName() == "file" {
			filename, content = part.FileName(), string(value)
			continue
		}
		fields = append(fields, core.FormField{Name: part.FormName(), Value: string(value)})
	}
	return fields, filename, content
}

// Synthesis output is WAV whatever response_format asked for, so the bytes are
// labelled from the upstream header rather than the requested format.
func TestCreateSpeech_ForwardsRequestAndLabelsUpstreamFormat(t *testing.T) {
	url, capture := wavServer(t)
	provider := NewWithHTTPClient("", url, http.DefaultClient, llmclient.Hooks{})

	resp, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model:          "pocket-tts",
		Input:          "the task has completed successfully",
		Voice:          "alba",
		ResponseFormat: "mp3",
	})
	require.NoError(t, err)
	assert.Equal(t, "audio/wav", resp.ContentType)
	assert.Equal(t, "RIFF....WAVE", string(resp.Data))

	req := capture.Last(t)
	assert.Equal(t, http.MethodPost, req.Method)
	assert.Equal(t, "/v1/audio/speech", req.Path)
	sent := req.JSON(t)
	assert.Equal(t, "pocket-tts", sent["model"])
	assert.Equal(t, "the task has completed successfully", sent["input"])
	assert.Equal(t, "alba", sent["voice"])
}

// audio.cpp extends the OpenAI speech body with its own members; they reach it
// unchanged rather than being dropped as unknown (ADR-0011 rule 1).
func TestCreateSpeech_ForwardsNativeExtraFields(t *testing.T) {
	url, capture := wavServer(t)
	provider := NewWithHTTPClient("", url, http.DefaultClient, llmclient.Hooks{})

	var req core.AudioSpeechRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"model": "pocket-tts",
		"input": "cloned from an inline reference",
		"voice_ref": {"type": "path", "path": "voices/alba.wav"},
		"reference_text": "transcript of the reference audio",
		"seed": "12345678901234567890"
	}`), &req))

	_, err := provider.CreateSpeech(context.Background(), &req)
	require.NoError(t, err)

	sent := capture.Last(t).JSON(t)
	assert.Equal(t, "12345678901234567890", sent["seed"])
	assert.Equal(t, "transcript of the reference audio", sent["reference_text"])
	assert.Equal(t, map[string]any{"type": "path", "path": "voices/alba.wav"}, sent["voice_ref"])
}

func TestCreateSpeech_ValidatesRequest(t *testing.T) {
	url, capture := wavServer(t)
	provider := NewWithHTTPClient("", url, http.DefaultClient, llmclient.Hooks{})

	tests := []struct {
		name    string
		req     *core.AudioSpeechRequest
		wantErr string
	}{
		{name: "nil request", req: nil, wantErr: "audio speech request is required"},
		{name: "no model", req: &core.AudioSpeechRequest{Input: "hi"}, wantErr: "model is required"},
		{name: "no input", req: &core.AudioSpeechRequest{Model: "pocket-tts"}, wantErr: "input is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := provider.CreateSpeech(context.Background(), tt.req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
	assert.Zero(t, capture.Count(), "invalid requests must not reach the upstream")
}

func TestCreateTranscription_SendsMultipartUpload(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK,
		`{"text":"the task has completed successfully","timing":{"wall_ms":216.4}}`)
	provider := NewWithHTTPClient("", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:    "moonshine-tiny",
		Filename: "speech.wav",
		File:     []byte("RIFF-bytes"),
		Language: "en",
		Prompt:   "GoModel",
		Fields:   []core.FormField{{Name: "stream", Value: "true"}, {Name: "busy_timeout_ms", Value: "5000"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "application/json", resp.ContentType)
	assert.JSONEq(t, `{"text":"the task has completed successfully","timing":{"wall_ms":216.4}}`, string(resp.Data))

	req := capture.Last(t)
	assert.Equal(t, "/v1/audio/transcriptions", req.Path)
	fields, filename, content := multipartFields(t, req)
	assert.Equal(t, "speech.wav", filename)
	assert.Equal(t, "RIFF-bytes", content)
	assert.Equal(t, []core.FormField{
		{Name: "model", Value: "moonshine-tiny"},
		{Name: "language", Value: "en"},
		{Name: "prompt", Value: "GoModel"},
		{Name: "stream", Value: "true"},
		{Name: "busy_timeout_ms", Value: "5000"},
	}, fields)
}

// A request with no file part is not an error here: audio.cpp transcribes a
// path on its own machine, which is the case behind the gateway no longer
// demanding an upload.
func TestCreateTranscription_SendsJSONForAServerLocalPath(t *testing.T) {
	for _, field := range audioPathFields {
		t.Run(field, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, `{"text":"hello"}`)
			provider := NewWithHTTPClient("", server.URL, server.Client(), llmclient.Hooks{})

			resp, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
				Model:    "moonshine-tiny",
				Language: "en",
				Prompt:   "GoModel",
				Fields: []core.FormField{
					{Name: field, Value: "/srv/audio/input.wav"},
					{Name: "stream", Value: "true"},
					{Name: "busy_timeout_ms", Value: "5000"},
					{Name: "unknown_form_only", Value: "x"},
				},
			})
			require.NoError(t, err)
			assert.JSONEq(t, `{"text":"hello"}`, string(resp.Data))

			req := capture.Last(t)
			assert.Equal(t, "/v1/audio/transcriptions", req.Path)
			assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
			sent := req.JSON(t)
			assert.Equal(t, "moonshine-tiny", sent["model"])
			assert.Equal(t, "/srv/audio/input.wav", sent["audio"])
			assert.Equal(t, "en", sent["language"])
			// audio.cpp names OpenAI's recognition-context "prompt" field "text".
			assert.Equal(t, "GoModel", sent["text"])
			// stream is a boolean and busy_timeout_ms a number upstream; a form
			// string in either place fails the request.
			assert.Equal(t, true, sent["stream"])
			assert.Equal(t, float64(5000), sent["busy_timeout_ms"])
			assert.NotContains(t, sent, "unknown_form_only")
		})
	}
}

func TestCreateTranscription_ValidatesRequest(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"text":"hello"}`)
	provider := NewWithHTTPClient("", server.URL, server.Client(), llmclient.Hooks{})

	tests := []struct {
		name    string
		req     *core.AudioTranscriptionRequest
		wantErr string
	}{
		{name: "nil request", req: nil, wantErr: "audio transcription request is required"},
		{name: "no model", req: &core.AudioTranscriptionRequest{File: []byte("RIFF")}, wantErr: "model is required"},
		{
			name:    "neither an upload nor a path",
			req:     &core.AudioTranscriptionRequest{Model: "moonshine-tiny"},
			wantErr: "file is required, or an audio field naming a path the audio.cpp server can read",
		},
		{
			name: "non-numeric busy timeout",
			req: &core.AudioTranscriptionRequest{
				Model:  "moonshine-tiny",
				Fields: []core.FormField{{Name: "audio", Value: "/srv/audio/input.wav"}, {Name: "busy_timeout_ms", Value: "soon"}},
			},
			wantErr: "busy_timeout_ms must be an integer",
		},
		{
			name:    "unsupported response format",
			req:     &core.AudioTranscriptionRequest{Model: "moonshine-tiny", File: []byte("RIFF"), ResponseFormat: "srt"},
			wantErr: "audiocpp transcription supports json, verbose_json, or text response formats",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := provider.CreateTranscription(context.Background(), tt.req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
	assert.Zero(t, capture.Count(), "invalid requests must not reach the upstream")
}

func TestCreateTranscription_TextFormatReturnsPlainTranscript(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"text":"hello there","timing":{"wall_ms":1}}`)
	provider := NewWithHTTPClient("", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:          "moonshine-tiny",
		File:           []byte("RIFF"),
		ResponseFormat: "text",
	})
	require.NoError(t, err)
	assert.Equal(t, "text/plain; charset=utf-8", resp.ContentType)
	assert.Equal(t, "hello there", string(resp.Data))
}

// A streamed transcription answers with the OpenAI transcript events, so the
// body is relayed under its own content type instead of being labelled JSON.
func TestCreateTranscription_RelaysStreamedEvents(t *testing.T) {
	server, _ := providertest.SSEServer(t, "event: transcript.text.delta\ndata: {\"delta\":\"hi\"}\n\ndata: [DONE]\n\n")
	provider := NewWithHTTPClient("", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:  "moonshine-tiny",
		File:   []byte("RIFF"),
		Fields: []core.FormField{{Name: "stream", Value: "true"}},
	})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(resp.ContentType, "text/event-stream"), "content type %q", resp.ContentType)
	assert.Contains(t, string(resp.Data), "transcript.text.delta")
}

// audio.cpp reports errors in the OpenAI envelope, so the client-level parser
// surfaces the message rather than the raw body.
func TestCreateTranscription_SurfacesUpstreamError(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusBadRequest,
		`{"error":{"message":"only WAV audio uploads are currently supported for transcription","type":"invalid_request_error"}}`)
	provider := NewWithHTTPClient("", server.URL, server.Client(), llmclient.Hooks{})

	_, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:    "moonshine-tiny",
		Filename: "speech.mp3",
		File:     []byte("ID3"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only WAV audio uploads are currently supported")
}

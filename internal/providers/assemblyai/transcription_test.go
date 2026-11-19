package assemblyai

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

var testAudio = []byte("RIFF-test-audio")

const transcriptPath = "/v2/transcript/" + testTranscriptID

func transcriptionRequest() *core.AudioTranscriptionRequest {
	return &core.AudioTranscriptionRequest{
		Model:    "assemblyai-stt-3-7",
		Filename: "speech.wav",
		File:     testAudio,
	}
}

func TestCreateTranscription_UploadsSubmitsPollsAndDeletes(t *testing.T) {
	fake := &fakeAssemblyAI{pendingPolls: 2}
	url, client, capture := fake.start(t)
	provider := newTestProvider("aai_test", url, client)

	req := transcriptionRequest()
	req.Language = "en"
	req.Prompt = "League of Legends roles"
	req.Temperature = "0.2"
	resp, err := provider.CreateTranscription(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, []string{
		"POST /v2/upload",
		"POST /v2/transcript",
		"GET " + transcriptPath,
		"GET " + transcriptPath,
		"GET " + transcriptPath,
		"DELETE " + transcriptPath,
	}, calls(capture))

	recorded := capture.All()
	for _, call := range recorded {
		assert.Equal(t, "aai_test", call.Header.Get("Authorization"), "%s %s must send the bare API key", call.Method, call.Path)
	}
	assert.Equal(t, "application/octet-stream", recorded[0].Header.Get("Content-Type"))
	assert.Equal(t, testAudio, recorded[0].Body)
	assert.Equal(t, map[string]any{
		"audio_url":     testUploadURL,
		"speech_models": []any{"assemblyai-stt-3-7"},
		"language_code": "en",
		"prompt":        "League of Legends roles",
		"temperature":   0.2,
	}, recorded[1].JSON(t))

	assert.Equal(t, "application/json", resp.ContentType)
	assert.JSONEq(t, `{
		"text": "Smoke from hundreds of wildfires. Skylines are obscured.",
		"usage": {"type": "duration", "seconds": 12}
	}`, string(resp.Data))
}

func TestCreateTranscription_OmitsUnsetOptionalFields(t *testing.T) {
	url, client, capture := (&fakeAssemblyAI{}).start(t)
	provider := newTestProvider("key", url, client)

	req := transcriptionRequest()
	req.Model = " universal-2 "
	_, err := provider.CreateTranscription(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, map[string]any{
		"audio_url":     testUploadURL,
		"speech_models": []any{"universal-2"},
	}, capture.All()[1].JSON(t))
}

func TestCreateTranscription_UploadsFromFileReader(t *testing.T) {
	url, client, capture := (&fakeAssemblyAI{}).start(t)
	provider := newTestProvider("key", url, client)

	req := transcriptionRequest()
	req.File = nil
	req.FileReader = strings.NewReader("streamed-audio")
	_, err := provider.CreateTranscription(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "streamed-audio", string(capture.All()[0].Body))
}

func TestCreateTranscription_ShapesResponseFormats(t *testing.T) {
	const text = "Smoke from hundreds of wildfires. Skylines are obscured."
	const segments = `[
		{"id": 0, "seek": 0, "start": 0.25, "end": 6.35, "text": "Smoke from hundreds of wildfires.",
		 "tokens": [], "temperature": 0, "avg_logprob": 0, "compression_ratio": 0, "no_speech_prob": 0},
		{"id": 1, "seek": 0, "start": 6.5, "end": 11.5, "text": "Skylines are obscured.",
		 "tokens": [], "temperature": 0, "avg_logprob": 0, "compression_ratio": 0, "no_speech_prob": 0}
	]`
	const words = `[
		{"word": "Smoke", "start": 0.25, "end": 0.65},
		{"word": "obscured.", "start": 11.05, "end": 11.5}
	]`
	verbose := func(members string) string {
		return `{"task": "transcribe", "language": "en_us", "duration": 12, "text": "` + text + `",
			"usage": {"type": "duration", "seconds": 12}, ` + members + `}`
	}

	tests := []struct {
		name          string
		format        string
		granularities []string
		wantExport    string // the extra call made for the format, if any
		wantContent   string
		wantJSON      string
		wantBody      string
	}{
		{name: "text", format: "text", wantContent: "text/plain; charset=utf-8", wantBody: text},
		{
			name: "srt", format: "SRT", wantExport: "GET " + transcriptPath + "/srt",
			wantContent: "text/plain; charset=utf-8",
			wantBody:    "1\n00:00:00,250 --> 00:00:06,350\nSmoke from hundreds of wildfires.\n",
		},
		{
			name: "vtt", format: "vtt", wantExport: "GET " + transcriptPath + "/vtt",
			wantContent: "text/plain; charset=utf-8",
			wantBody:    "WEBVTT\n\n00:00.250 --> 00:06.350\nSmoke from hundreds of wildfires.\n",
		},
		{
			name: "verbose_json defaults to segments", format: "verbose_json",
			wantExport:  "GET " + transcriptPath + "/sentences",
			wantContent: "application/json", wantJSON: verbose(`"segments": ` + segments),
		},
		{
			name: "verbose_json with word granularity", format: "verbose_json", granularities: []string{"word"},
			wantContent: "application/json", wantJSON: verbose(`"words": ` + words),
		},
		{
			name: "verbose_json with both granularities", format: "verbose_json", granularities: []string{"word", "segment"},
			wantExport:  "GET " + transcriptPath + "/sentences",
			wantContent: "application/json", wantJSON: verbose(`"segments": ` + segments + `, "words": ` + words),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, client, capture := (&fakeAssemblyAI{}).start(t)
			provider := newTestProvider("key", url, client)

			req := transcriptionRequest()
			req.ResponseFormat = tt.format
			req.TimestampGranularities = tt.granularities
			resp, err := provider.CreateTranscription(context.Background(), req)
			require.NoError(t, err)

			want := []string{"POST /v2/upload", "POST /v2/transcript", "GET " + transcriptPath}
			if tt.wantExport != "" {
				want = append(want, tt.wantExport)
			}
			assert.Equal(t, append(want, "DELETE "+transcriptPath), calls(capture))
			assert.Equal(t, tt.wantContent, resp.ContentType)
			if tt.wantJSON != "" {
				assert.JSONEq(t, tt.wantJSON, string(resp.Data))
			} else {
				assert.Equal(t, tt.wantBody, string(resp.Data))
			}
		})
	}
}

// Without audio_duration there is no provider-reported usage to relay, and
// verbose_json falls back to the end of the last word.
func TestCreateTranscription_HandlesMissingAudioDuration(t *testing.T) {
	final := `{"id":"` + testTranscriptID + `","status":"completed","text":"Hi.","audio_duration":null,
		"words":[{"text":"Hi.","start":100,"end":900}]}`

	t.Run("json", func(t *testing.T) {
		url, client, _ := (&fakeAssemblyAI{final: final}).start(t)
		resp, err := newTestProvider("key", url, client).CreateTranscription(context.Background(), transcriptionRequest())
		require.NoError(t, err)
		assert.JSONEq(t, `{"text":"Hi."}`, string(resp.Data))
	})

	t.Run("verbose_json", func(t *testing.T) {
		url, client, _ := (&fakeAssemblyAI{final: final}).start(t)
		req := transcriptionRequest()
		req.ResponseFormat = "verbose_json"
		req.TimestampGranularities = []string{"word"}
		resp, err := newTestProvider("key", url, client).CreateTranscription(context.Background(), req)
		require.NoError(t, err)
		assert.JSONEq(t, `{"task":"transcribe","language":"","duration":0.9,"text":"Hi.",
			"words":[{"word":"Hi.","start":0.1,"end":0.9}]}`, string(resp.Data))
	})
}

func TestCreateTranscription_MapsFailedJobs(t *testing.T) {
	tests := []struct {
		name       string
		message    string
		wantType   core.ErrorType
		wantStatus int
	}{
		{
			name:       "audio the job could not use is the caller's error",
			message:    "Audio duration is too short.",
			wantType:   core.ErrorTypeInvalidRequest,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "an internal failure is the provider's error",
			message:    "Internal server error. Please retry your request.",
			wantType:   core.ErrorTypeProvider,
			wantStatus: http.StatusBadGateway,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeAssemblyAI{final: `{"id":"` + testTranscriptID + `","status":"error","error":"` + tt.message + `"}`}
			url, client, capture := fake.start(t)

			_, err := newTestProvider("key", url, client).CreateTranscription(context.Background(), transcriptionRequest())

			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, tt.wantType, gatewayErr.Type)
			assert.Equal(t, tt.wantStatus, gatewayErr.StatusCode)
			assert.Equal(t, tt.message, gatewayErr.Message)
			assert.Equal(t, "assemblyai", gatewayErr.Provider)
			assert.Equal(t, "DELETE "+transcriptPath, calls(capture)[capture.Count()-1], "a failed job is deleted too")
		})
	}
}

func TestCreateTranscription_TimesOutAndDeletesUnfinishedJob(t *testing.T) {
	url, client, capture := (&fakeAssemblyAI{pendingPolls: 1 << 30}).start(t)
	provider := newTestProvider("key", url, client)
	provider.jobTimeout = 30 * time.Millisecond

	_, err := provider.CreateTranscription(context.Background(), transcriptionRequest())

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusGatewayTimeout, gatewayErr.StatusCode)
	assert.Equal(t, "DELETE "+transcriptPath, calls(capture)[capture.Count()-1])
}

func TestCreateTranscription_DeletesJobWhenCallerCancels(t *testing.T) {
	url, client, capture := (&fakeAssemblyAI{pendingPolls: 1 << 30}).start(t)
	provider := newTestProvider("key", url, client)
	provider.pollInterval = time.Minute

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	_, err := provider.CreateTranscription(ctx, transcriptionRequest())

	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []string{"POST /v2/upload", "POST /v2/transcript", "DELETE " + transcriptPath}, calls(capture))
}

func TestCreateTranscription_ReturnsTranscriptWhenDeleteFails(t *testing.T) {
	server, capture := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /v2/upload":
			_, _ = io.WriteString(w, `{"upload_url":"`+testUploadURL+`"}`)
		case "POST /v2/transcript":
			_, _ = io.WriteString(w, completedTranscript)
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"Transcript lookup error, transcript id not found"}`)
		}
	})
	provider := newTestProvider("key", server.URL, server.Client())

	req := transcriptionRequest()
	req.ResponseFormat = "text"
	resp, err := provider.CreateTranscription(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "Smoke from hundreds of wildfires. Skylines are obscured.", string(resp.Data))
	assert.Equal(t, []string{"POST /v2/upload", "POST /v2/transcript", "DELETE " + transcriptPath}, calls(capture))
}

func TestCreateTranscription_MapsUpstreamErrors(t *testing.T) {
	tests := []struct {
		name        string
		failOn      string
		status      int
		body        string
		wantType    core.ErrorType
		wantMessage string
		wantCalls   []string
	}{
		{
			name:        "rejected key on upload",
			failOn:      "POST /v2/upload",
			status:      http.StatusUnauthorized,
			body:        `{"error":"Authentication error, API token missing/invalid"}`,
			wantType:    core.ErrorTypeAuthentication,
			wantMessage: "Authentication error, API token missing/invalid",
			wantCalls:   []string{"POST /v2/upload"},
		},
		{
			name:        "rejected parameter on submit",
			failOn:      "POST /v2/transcript",
			status:      http.StatusBadRequest,
			body:        `{"error":"Language code xx is not supported."}`,
			wantType:    core.ErrorTypeInvalidRequest,
			wantMessage: "Language code xx is not supported.",
			wantCalls:   []string{"POST /v2/upload", "POST /v2/transcript"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method+" "+r.URL.Path == tt.failOn {
					w.WriteHeader(tt.status)
					_, _ = io.WriteString(w, tt.body)
					return
				}
				_, _ = io.WriteString(w, `{"upload_url":"`+testUploadURL+`"}`)
			})

			_, err := newTestProvider("key", server.URL, server.Client()).CreateTranscription(context.Background(), transcriptionRequest())

			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, tt.wantType, gatewayErr.Type)
			assert.Equal(t, tt.status, gatewayErr.StatusCode)
			assert.Equal(t, tt.wantMessage, gatewayErr.Message)
			assert.Equal(t, tt.wantCalls, calls(capture))
		})
	}
}

func TestCreateTranscription_RejectsUnusableUpstreamAnswers(t *testing.T) {
	tests := []struct {
		name       string
		upload     string
		transcript string
		want       string
	}{
		{name: "no upload_url", upload: `{}`, want: "no upload_url"},
		{name: "no transcript id", upload: `{"upload_url":"` + testUploadURL + `"}`, transcript: `{"status":"queued"}`, want: "without an id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, _ := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v2/upload" {
					_, _ = io.WriteString(w, tt.upload)
					return
				}
				_, _ = io.WriteString(w, tt.transcript)
			})

			_, err := newTestProvider("key", server.URL, server.Client()).CreateTranscription(context.Background(), transcriptionRequest())

			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, http.StatusBadGateway, gatewayErr.StatusCode)
			assert.Contains(t, gatewayErr.Message, tt.want)
		})
	}
}

// AssemblyAI only lets the project that uploaded a file transcribe it, so a
// rotating keyring must not split one job across keys.
func TestCreateTranscription_KeepsOneKeyForTheWholeJob(t *testing.T) {
	url, client, capture := (&fakeAssemblyAI{}).start(t)
	opts := providertest.Options(llmclient.Hooks{})
	opts.HTTPClient = client
	opts.Keys = providers.NewKeyring("key-a", "key-b")
	provider := New(providers.ProviderConfig{BaseURL: url}, opts).(*Provider)
	provider.pollInterval = time.Millisecond

	req := transcriptionRequest()
	req.ResponseFormat = "srt"
	for range 2 {
		_, err := provider.CreateTranscription(context.Background(), req)
		require.NoError(t, err)
	}

	// Each job is upload, submit, poll, export, delete.
	const perJob = 5
	recorded := capture.All()
	require.Len(t, recorded, 2*perJob)
	firstKey, secondKey := recorded[0].Header.Get("Authorization"), recorded[perJob].Header.Get("Authorization")
	assert.NotEqual(t, firstKey, secondKey, "jobs still rotate keys")
	for i, call := range recorded {
		want := firstKey
		if i >= perJob {
			want = secondKey
		}
		assert.Equal(t, want, call.Header.Get("Authorization"), "call %d: %s %s", i, call.Method, call.Path)
	}
}

func TestCreateTranscription_ValidatesRequest(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*core.AudioTranscriptionRequest)
		want   string
	}{
		{"missing model", func(r *core.AudioTranscriptionRequest) { r.Model = " " }, "model is required"},
		{"missing file", func(r *core.AudioTranscriptionRequest) { r.File = nil }, "file is required"},
		{"unsupported response format", func(r *core.AudioTranscriptionRequest) { r.ResponseFormat = "diarized_json" }, "response formats"},
		{"temperature that is not a number", func(r *core.AudioTranscriptionRequest) { r.Temperature = "warm" }, "temperature must be a number"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, client, capture := (&fakeAssemblyAI{}).start(t)
			req := transcriptionRequest()
			tt.mutate(req)

			_, err := newTestProvider("key", url, client).CreateTranscription(context.Background(), req)

			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
			assert.Contains(t, gatewayErr.Message, tt.want)
			assert.Zero(t, capture.Count(), "an invalid request must not reach AssemblyAI")
		})
	}

	t.Run("nil request", func(t *testing.T) {
		_, err := newTestProvider("key", "https://unused.example", nil).CreateTranscription(context.Background(), nil)
		require.Error(t, err)
	})
}

func TestCreateSpeech_IsNotSupported(t *testing.T) {
	_, err := newTestProvider("key", "https://unused.example", nil).CreateSpeech(context.Background(), &core.AudioSpeechRequest{})

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
	assert.Contains(t, gatewayErr.Message, "does not support text-to-speech")
}

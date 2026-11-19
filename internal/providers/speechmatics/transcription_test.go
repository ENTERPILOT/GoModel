package speechmatics

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// helloTranscript is a json-v2 transcript of "Hello world. How are you?".
const helloTranscript = `{
	"format": "2.9",
	"job": {"created_at": "2026-06-23T13:33:24.230Z", "data_name": "hello.wav", "duration": 4, "id": "job1"},
	"metadata": {
		"created_at": "2026-06-23T13:33:24.879071Z",
		"type": "transcription",
		"transcription_config": {"language": "en", "model": "enhanced"},
		"language_pack_info": {"word_delimiter": " ", "writing_direction": "left-to-right"}
	},
	"results": [
		{"type": "word", "start_time": 0.5, "end_time": 0.9, "alternatives": [{"content": "Hello", "confidence": 0.95, "language": "en"}]},
		{"type": "word", "start_time": 1.0, "end_time": 1.4, "alternatives": [{"content": "world", "confidence": 0.9, "language": "en"}]},
		{"type": "punctuation", "start_time": 1.4, "end_time": 1.4, "is_eos": true, "attaches_to": "previous", "alternatives": [{"content": ".", "confidence": 1, "language": "en"}]},
		{"type": "word", "start_time": 2.0, "end_time": 2.3, "alternatives": [{"content": "How", "confidence": 1, "language": "en"}]},
		{"type": "word", "start_time": 2.3, "end_time": 2.5, "alternatives": [{"content": "are", "confidence": 1, "language": "en"}]},
		{"type": "word", "start_time": 2.5, "end_time": 2.9, "alternatives": [{"content": "you", "confidence": 1, "language": "en"}]},
		{"type": "punctuation", "start_time": 2.9, "end_time": 2.9, "is_eos": true, "attaches_to": "previous", "alternatives": [{"content": "?", "confidence": 1, "language": "en"}]}
	]
}`

const helloSRT = "1\n00:00:00,500 --> 00:00:01,400\nHello world.\n\n2\n00:00:02,000 --> 00:00:02,900\nHow are you?\n"

func transcriptionRequest() *core.AudioTranscriptionRequest {
	return &core.AudioTranscriptionRequest{
		Model:    "enhanced",
		Filename: "hello.wav",
		File:     []byte("RIFF-audio"),
		Language: "en",
	}
}

// jobUpload decodes the multipart body of a recorded POST /jobs.
func jobUpload(t *testing.T, req providertest.Recorded) (config, filename string, audio []byte) {
	t.Helper()
	_, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	require.NoError(t, err)

	reader := multipart.NewReader(bytes.NewReader(req.Body), params["boundary"])
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			return config, filename, audio
		}
		require.NoError(t, err)
		data, err := io.ReadAll(part)
		require.NoError(t, err)
		switch part.FormName() {
		case "config":
			config = string(data)
		case "data_file":
			filename, audio = part.FileName(), data
		default:
			assert.Fail(t, "unexpected multipart field", part.FormName())
		}
	}
}

func TestCreateTranscription_RunsBatchJob(t *testing.T) {
	api := newFakeBatchAPI(t, &fakeBatchAPI{
		statuses:    []string{"running", "running", "done"},
		transcripts: map[string]string{"json-v2": helloTranscript},
	})
	provider := newTestProvider(api)

	resp, err := provider.CreateTranscription(context.Background(), transcriptionRequest())
	require.NoError(t, err)

	assert.Equal(t, []string{
		"POST /jobs",
		"GET /jobs/job1",
		"GET /jobs/job1",
		"GET /jobs/job1",
		"GET /jobs/job1/transcript?format=json-v2",
	}, api.calls(), "a finished job is left in place, not deleted")

	recorded := api.capture.All()
	for _, req := range recorded {
		assert.Equal(t, "Bearer sm_test", req.Header.Get("Authorization"), "%s %s", req.Method, req.Path)
	}
	config, filename, audio := jobUpload(t, recorded[0])
	assert.JSONEq(t, `{"type":"transcription","transcription_config":{"model":"enhanced","language":"en"}}`, config)
	assert.Equal(t, "hello.wav", filename)
	assert.Equal(t, []byte("RIFF-audio"), audio)

	assert.Equal(t, "application/json", resp.ContentType)
	assert.JSONEq(t, `{"text":"Hello world. How are you?","usage":{"type":"duration","seconds":4}}`, string(resp.Data))
}

func TestNewJobConfig_MapsModelAndLanguage(t *testing.T) {
	const bestGuess = `,"language_identification_config":{"low_confidence_action":"allow"}`
	tests := []struct {
		name     string
		model    string
		language string
		want     string
	}{
		{"language is forwarded", "enhanced", "de", `"transcription_config":{"model":"enhanced","language":"de"}`},
		{"language is normalized", "standard", " EN ", `"transcription_config":{"model":"standard","language":"en"}`},
		{"no language asks for the best identified one", "enhanced", "", `"transcription_config":{"model":"enhanced","language":"auto"}` + bestGuess},
		{"auto is identification too", "enhanced", "auto", `"transcription_config":{"model":"enhanced","language":"auto"}` + bestGuess},
		{"OpenAI's zh is Speechmatics' Mandarin", "enhanced", "zh", `"transcription_config":{"model":"enhanced","language":"cmn"}`},
		{"language packs pass through", "enhanced", "cmn_en", `"transcription_config":{"model":"enhanced","language":"cmn_en"}`},
		{"multilingual model takes multi", "melia-1", "", `"transcription_config":{"model":"melia-1","language":"multi"}`},
		{"multilingual model takes the language as a hint", "melia-1", "ar", `"transcription_config":{"model":"melia-1","language":"multi","language_hints":["ar"]}`},
		{"multilingual model rejects auto, so it is not sent", "oak-1", "auto", `"transcription_config":{"model":"oak-1","language":"multi"}`},
		{"unknown model is sent as is", "enhanced-2", "en", `"transcription_config":{"model":"enhanced-2","language":"en"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(newJobConfig(tt.model, tt.language))
			require.NoError(t, err)
			assert.JSONEq(t, `{"type":"transcription",`+tt.want+`}`, string(got))
		})
	}
}

func TestCreateTranscription_ShapesResponseFormats(t *testing.T) {
	const segments = `[
		{"id": 0, "start": 0.5, "end": 1.4, "text": "Hello world."},
		{"id": 1, "start": 2.0, "end": 2.9, "text": "How are you?"}
	]`
	tests := []struct {
		name            string
		format          string
		granularities   []string
		wantUpstream    string
		wantContentType string
		wantJSON        string
		wantText        string
	}{
		{
			name: "json is the default", format: "",
			wantUpstream: "json-v2", wantContentType: "application/json",
			wantJSON: `{"text":"Hello world. How are you?","usage":{"type":"duration","seconds":4}}`,
		},
		{
			name: "text", format: "text",
			wantUpstream: "json-v2", wantContentType: "text/plain; charset=utf-8",
			wantText: "Hello world. How are you?",
		},
		{
			name: "verbose_json reports sentences as segments", format: "verbose_json",
			wantUpstream: "json-v2", wantContentType: "application/json",
			wantJSON: `{"task":"transcribe","language":"en","duration":4,"text":"Hello world. How are you?","segments":` + segments + `}`,
		},
		{
			name: "verbose_json adds words on request", format: "VERBOSE_JSON", granularities: []string{"segment", "word"},
			wantUpstream: "json-v2", wantContentType: "application/json",
			wantJSON: `{"task":"transcribe","language":"en","duration":4,"text":"Hello world. How are you?","segments":` + segments + `,"words":[
				{"word":"Hello","start":0.5,"end":0.9},
				{"word":"world","start":1.0,"end":1.4},
				{"word":"How","start":2.0,"end":2.3},
				{"word":"are","start":2.3,"end":2.5},
				{"word":"you","start":2.5,"end":2.9}
			]}`,
		},
		{
			name: "srt is Speechmatics' own", format: "srt",
			wantUpstream: "srt", wantContentType: "text/plain; charset=utf-8",
			wantText: helloSRT,
		},
		{
			name: "vtt is converted from srt", format: "vtt",
			wantUpstream: "srt", wantContentType: "text/plain; charset=utf-8",
			wantText: "WEBVTT\n\n1\n00:00:00.500 --> 00:00:01.400\nHello world.\n\n2\n00:00:02.000 --> 00:00:02.900\nHow are you?\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeBatchAPI(t, &fakeBatchAPI{
				transcripts: map[string]string{"json-v2": helloTranscript, "srt": helloSRT},
			})
			req := transcriptionRequest()
			req.ResponseFormat = tt.format
			req.TimestampGranularities = tt.granularities

			resp, err := newTestProvider(api).CreateTranscription(context.Background(), req)
			require.NoError(t, err)

			assert.Equal(t, tt.wantUpstream, api.capture.Last(t).Query.Get("format"))
			assert.Equal(t, tt.wantContentType, resp.ContentType)
			if tt.wantJSON != "" {
				assert.JSONEq(t, tt.wantJSON, string(resp.Data))
			} else {
				assert.Equal(t, tt.wantText, string(resp.Data))
			}
		})
	}
}

func TestCreateTranscription_ReportsJobsThatEndWithoutATranscript(t *testing.T) {
	tests := []struct {
		name        string
		status      string
		jobErrors   string
		wantType    core.ErrorType
		wantStatus  int
		wantMessage string
	}{
		{
			name: "rejected with a reason", status: "rejected",
			jobErrors:   `[{"message":"No speech found for language identification","timestamp":"2023-02-27T11:59:20.321Z"}]`,
			wantType:    core.ErrorTypeInvalidRequest,
			wantStatus:  http.StatusBadRequest,
			wantMessage: "speechmatics rejected the transcription job: No speech found for language identification",
		},
		{
			name: "rejected without a reason", status: "rejected",
			wantType:    core.ErrorTypeInvalidRequest,
			wantStatus:  http.StatusBadRequest,
			wantMessage: "speechmatics rejected the transcription job",
		},
		{
			name: "deleted upstream", status: "deleted",
			wantType:    core.ErrorTypeProvider,
			wantStatus:  http.StatusBadGateway,
			wantMessage: "speechmatics transcription job was deleted before it finished",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeBatchAPI(t, &fakeBatchAPI{statuses: []string{"running", tt.status}, jobErrors: tt.jobErrors})

			_, err := newTestProvider(api).CreateTranscription(context.Background(), transcriptionRequest())

			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, tt.wantType, gatewayErr.Type)
			assert.Equal(t, tt.wantStatus, gatewayErr.StatusCode)
			assert.Equal(t, tt.wantMessage, gatewayErr.Message)
			assert.Equal(t, "speechmatics", gatewayErr.Provider)
			assert.Equal(t, []string{"POST /jobs", "GET /jobs/job1", "GET /jobs/job1"}, api.calls(),
				"an ended job needs no transcript fetch and no termination")
		})
	}
}

// A job nobody waits for anymore must not keep running upstream.
func TestCreateTranscription_TerminatesTheJobWhenTheRequestIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api := newFakeBatchAPI(t, &fakeBatchAPI{
		statuses: []string{"running"},
		onStatus: func(*http.Request) { cancel() },
	})

	_, err := newTestProvider(api).CreateTranscription(ctx, transcriptionRequest())

	require.ErrorIs(t, err, context.Canceled)
	calls := api.calls()
	assert.Equal(t, "DELETE /jobs/job1?force=true", calls[len(calls)-1])
	assert.Equal(t, "Bearer sm_test", api.capture.Last(t).Header.Get("Authorization"))
}

func TestCreateTranscription_TerminatesTheJobAfterTheTimeout(t *testing.T) {
	api := newFakeBatchAPI(t, &fakeBatchAPI{
		statuses: []string{"running"},
		// Speechmatics holds a status request open while the job runs.
		onStatus: func(r *http.Request) { <-r.Context().Done() },
	})
	provider := newTestProvider(api)
	provider.jobTimeout = 500 * time.Millisecond

	_, err := provider.CreateTranscription(context.Background(), transcriptionRequest())

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusGatewayTimeout, gatewayErr.StatusCode)
	assert.Equal(t, "speechmatics transcription did not finish within 500ms", gatewayErr.Message)
	assert.Equal(t, []string{"POST /jobs", "GET /jobs/job1", "DELETE /jobs/job1?force=true"}, api.calls())
}

func TestCreateTranscription_TerminatesTheJobWhenItsStatusIsUnavailable(t *testing.T) {
	// The fake only knows job1, so the status of this job answers 404.
	api := newFakeBatchAPI(t, &fakeBatchAPI{create: func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"elsewhere"}`)
	}})

	_, err := newTestProvider(api).CreateTranscription(context.Background(), transcriptionRequest())

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusNotFound, gatewayErr.StatusCode)
	assert.Equal(t, []string{"POST /jobs", "GET /jobs/elsewhere", "DELETE /jobs/elsewhere?force=true"}, api.calls())
}

func TestCreateTranscription_TerminatesTheJobWhenTheBreakerOpens(t *testing.T) {
	api := newFakeBatchAPI(t, &fakeBatchAPI{statusFailure: http.StatusInternalServerError})
	opts := providertest.Options(llmclient.Hooks{})
	opts.HTTPClient = api.client
	opts.Resilience.Retry.MaxRetries = 0
	opts.Resilience.CircuitBreaker.FailureThreshold = 1
	provider := New(providers.ProviderConfig{APIKey: "sm_test", BaseURL: api.url}, opts).(*Provider)

	_, err := provider.CreateTranscription(context.Background(), transcriptionRequest())

	require.Error(t, err)
	assert.Equal(t, []string{"POST /jobs", "GET /jobs/job1", "DELETE /jobs/job1?force=true"}, api.calls())
}

func TestCreateTranscription_MapsSubmissionErrors(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		wantType    core.ErrorType
		wantMessage string
	}{
		{
			name: "rejected config keeps the detail", status: http.StatusBadRequest,
			body:        `{"code":400,"error":"Job rejected","detail":"Job config JSON is invalid"}`,
			wantType:    core.ErrorTypeInvalidRequest,
			wantMessage: "Job rejected: Job config JSON is invalid",
		},
		{
			name: "entitlement", status: http.StatusForbidden,
			body:        `{"code":403,"detail":"Entitlement check failed","error":"Forbidden"}`,
			wantType:    core.ErrorTypeAuthentication,
			wantMessage: "Forbidden: Entitlement check failed",
		},
		{
			name: "error without detail", status: http.StatusUnauthorized,
			body:        `{"code":401,"error":"Permission Denied"}`,
			wantType:    core.ErrorTypeAuthentication,
			wantMessage: "Permission Denied",
		},
		{
			name: "rate limit", status: http.StatusTooManyRequests,
			body:        `{"code":429,"error":"Rate Limited"}`,
			wantType:    core.ErrorTypeRateLimit,
			wantMessage: "Rate Limited",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeBatchAPI(t, &fakeBatchAPI{create: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}})

			_, err := newTestProvider(api).CreateTranscription(context.Background(), transcriptionRequest())

			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, tt.wantType, gatewayErr.Type)
			assert.Equal(t, tt.status, gatewayErr.StatusCode)
			assert.Equal(t, tt.wantMessage, gatewayErr.Message)
			assert.Equal(t, []string{"POST /jobs"}, api.calls(), "a failed upload is not retried: a retry could start a second job")
		})
	}
}

func TestCreateTranscription_RejectsAJobResponseWithoutAnID(t *testing.T) {
	api := newFakeBatchAPI(t, &fakeBatchAPI{create: func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{}`)
	}})

	_, err := newTestProvider(api).CreateTranscription(context.Background(), transcriptionRequest())

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusBadGateway, gatewayErr.StatusCode)
	assert.Equal(t, []string{"POST /jobs"}, api.calls())
}

// A job belongs to the account that created it, so key rotation must not
// move its later calls to another key.
func TestCreateTranscription_KeepsOneAPIKeyPerJob(t *testing.T) {
	api := newFakeBatchAPI(t, &fakeBatchAPI{transcripts: map[string]string{"json-v2": helloTranscript}})
	provider := newTestProvider(api, "key-a", "key-b")

	for range 2 {
		_, err := provider.CreateTranscription(context.Background(), transcriptionRequest())
		require.NoError(t, err)
	}

	recorded := api.capture.All()
	require.Len(t, recorded, 6, "two jobs of three calls each")
	keys := make([]string, 0, len(recorded))
	for _, req := range recorded {
		keys = append(keys, req.Header.Get("Authorization"))
	}
	assert.Equal(t, []string{keys[0], keys[0], keys[0], keys[3], keys[3], keys[3]}, keys)
	assert.NotEqual(t, keys[0], keys[3], "the next job rotates to the other key")
}

func TestCreateTranscription_ValidatesTheRequest(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*core.AudioTranscriptionRequest)
		want   string
	}{
		{"model", func(r *core.AudioTranscriptionRequest) { r.Model = " " }, "model is required"},
		{"file", func(r *core.AudioTranscriptionRequest) { r.File = nil }, "file is required"},
		{"response format", func(r *core.AudioTranscriptionRequest) { r.ResponseFormat = "diarized_json" }, "supports json, text, verbose_json, srt, or vtt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeBatchAPI(t, &fakeBatchAPI{})
			req := transcriptionRequest()
			tt.mutate(req)

			_, err := newTestProvider(api).CreateTranscription(context.Background(), req)

			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
			assert.Contains(t, gatewayErr.Message, tt.want)
			assert.Zero(t, api.capture.Count(), "an invalid request never reaches Speechmatics")
		})
	}

	_, err := newTestProvider(newFakeBatchAPI(t, &fakeBatchAPI{})).CreateTranscription(context.Background(), nil)
	require.Error(t, err)
}

// prompt and temperature have no Speechmatics counterpart and only steer
// style, so they are dropped rather than failing the request.
func TestCreateTranscription_DropsHintsSpeechmaticsCannotTake(t *testing.T) {
	api := newFakeBatchAPI(t, &fakeBatchAPI{transcripts: map[string]string{"json-v2": helloTranscript}})
	req := transcriptionRequest()
	req.Prompt = "Speechmatics, GoModel"
	req.Temperature = "0.2"
	req.Fields = []core.FormField{{Name: "stream", Value: "true"}}

	_, err := newTestProvider(api).CreateTranscription(context.Background(), req)
	require.NoError(t, err)

	config, _, _ := jobUpload(t, api.capture.All()[0])
	assert.JSONEq(t, `{"type":"transcription","transcription_config":{"model":"enhanced","language":"en"}}`, config)
}

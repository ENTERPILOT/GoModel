package assemblyai

import (
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// newTestProvider builds the provider through its own constructor on a test
// transport, polling fast enough for a test.
func newTestProvider(apiKey, baseURL string, httpClient *http.Client) *Provider {
	opts := providertest.Options(llmclient.Hooks{})
	opts.HTTPClient = httpClient
	provider := New(providers.ProviderConfig{APIKey: apiKey, BaseURL: baseURL}, opts).(*Provider)
	provider.pollInterval = time.Millisecond
	return provider
}

const (
	testTranscriptID = "9ea68fd3-f953-42c1-9742-976c447fb463"
	testUploadURL    = "https://cdn.assemblyai.com/upload/f756988d-47e2-4ca3-96ce-04bb168f8f2a"
)

// completedTranscript follows the transcript object in AssemblyAI's API
// reference: millisecond word times and an integer audio_duration in seconds.
const completedTranscript = `{
	"id": "` + testTranscriptID + `",
	"status": "completed",
	"language_code": "en_us",
	"audio_duration": 12,
	"text": "Smoke from hundreds of wildfires. Skylines are obscured.",
	"words": [
		{"text": "Smoke", "start": 250, "end": 650, "confidence": 0.97, "speaker": null},
		{"text": "obscured.", "start": 11050, "end": 11500, "confidence": 0.99, "speaker": null}
	],
	"error": null
}`

const sentencesResponse = `{
	"id": "` + testTranscriptID + `",
	"confidence": 0.95,
	"audio_duration": 12,
	"sentences": [
		{"text": "Smoke from hundreds of wildfires.", "start": 250, "end": 6350, "confidence": 0.72, "words": [], "speaker": null},
		{"text": "Skylines are obscured.", "start": 6500, "end": 11500, "confidence": 0.9, "words": [], "speaker": null}
	]
}`

// fakeAssemblyAI is a pre-recorded API double: upload, create, poll, export,
// delete. The job reports "processing" for pendingPolls status checks and then
// answers final.
type fakeAssemblyAI struct {
	pendingPolls int32
	final        string
	polls        atomic.Int32
}

func (f *fakeAssemblyAI) start(t *testing.T) (string, *http.Client, *providertest.Capture) {
	t.Helper()
	if f.final == "" {
		f.final = completedTranscript
	}
	transcriptPath := "/v2/transcript/" + testTranscriptID
	server, capture := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /v2/upload":
			_, _ = io.WriteString(w, `{"upload_url":"`+testUploadURL+`"}`)
		case "POST /v2/transcript":
			_, _ = io.WriteString(w, `{"id":"`+testTranscriptID+`","status":"queued","text":null,"words":null,"audio_duration":null}`)
		case "GET " + transcriptPath:
			if f.polls.Add(1) <= f.pendingPolls {
				_, _ = io.WriteString(w, `{"id":"`+testTranscriptID+`","status":"processing"}`)
				return
			}
			_, _ = io.WriteString(w, f.final)
		case "GET " + transcriptPath + "/sentences":
			_, _ = io.WriteString(w, sentencesResponse)
		case "GET " + transcriptPath + "/srt":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "1\n00:00:00,250 --> 00:00:06,350\nSmoke from hundreds of wildfires.\n")
		case "GET " + transcriptPath + "/vtt":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "WEBVTT\n\n00:00.250 --> 00:06.350\nSmoke from hundreds of wildfires.\n")
		case "DELETE " + transcriptPath:
			_, _ = io.WriteString(w, `{"id":"`+testTranscriptID+`","status":"completed","text":"Deleted by user."}`)
		default:
			http.NotFound(w, r)
		}
	})
	return server.URL, server.Client(), capture
}

// calls lists the recorded requests as "METHOD path".
func calls(capture *providertest.Capture) []string {
	recorded := capture.All()
	out := make([]string, 0, len(recorded))
	for _, req := range recorded {
		out = append(out, req.Method+" "+req.Path)
	}
	return out
}

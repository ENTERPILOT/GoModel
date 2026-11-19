package speechmatics

import (
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// newTestProvider builds the provider through its own constructor on a test
// transport, with the pause between status requests cut to a millisecond.
func newTestProvider(server *fakeBatchAPI, keys ...string) *Provider {
	opts := providertest.Options(llmclient.Hooks{})
	opts.HTTPClient = server.client
	if len(keys) > 0 {
		opts.Keys = providers.NewKeyring(keys...)
	}
	provider := New(providers.ProviderConfig{APIKey: "sm_test", BaseURL: server.url}, opts).(*Provider)
	provider.pollInterval = time.Millisecond
	provider.maxPollInterval = time.Millisecond
	return provider
}

const testJobID = "job1"

// fakeBatchAPI is a Speechmatics Batch API double serving one job. Its zero
// value creates the job and reports it done on the first status request.
type fakeBatchAPI struct {
	url     string
	client  *http.Client
	capture *providertest.Capture

	// create overrides the POST /jobs answer.
	create http.HandlerFunc
	// statuses are the job statuses GET /jobs/job1 reports, in order; the last
	// one repeats. Empty means "done".
	statuses []string
	// jobErrors is the errors array of the job details.
	jobErrors string
	// onStatus runs before each status answer.
	onStatus func(r *http.Request)
	// statusFailure, when set, is the HTTP status every status request answers.
	statusFailure int
	// transcripts maps a transcript format to the body served for it.
	transcripts map[string]string

	mu    sync.Mutex
	polls int
}

func newFakeBatchAPI(t *testing.T, api *fakeBatchAPI) *fakeBatchAPI {
	t.Helper()
	server, capture := providertest.Server(t, api.serve)
	api.url, api.client, api.capture = server.URL, server.Client(), capture
	return api
}

func (f *fakeBatchAPI) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method + " " + r.URL.Path {
	case "POST /jobs":
		if f.create != nil {
			f.create(w, r)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"`+testJobID+`"}`)
	case "GET /jobs/" + testJobID:
		if f.onStatus != nil {
			f.onStatus(r)
		}
		if f.statusFailure != 0 {
			w.WriteHeader(f.statusFailure)
			_, _ = io.WriteString(w, `{"code":500,"error":"Internal Server Error"}`)
			return
		}
		jobErrors := f.jobErrors
		if jobErrors == "" {
			jobErrors = "[]"
		}
		_, _ = io.WriteString(w, `{"job":{"id":"`+testJobID+`","status":"`+f.nextStatus()+`","errors":`+jobErrors+`}}`)
	case "GET /jobs/" + testJobID + "/transcript":
		body, ok := f.transcripts[r.URL.Query().Get("format")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, body)
	case "DELETE /jobs/" + testJobID:
		_, _ = io.WriteString(w, `{"job":{"id":"`+testJobID+`","status":"deleted"}}`)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeBatchAPI) nextStatus() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.statuses) == 0 {
		return jobDone
	}
	status := f.statuses[min(f.polls, len(f.statuses)-1)]
	f.polls++
	return status
}

// calls lists the recorded requests as "METHOD /path?query".
func (f *fakeBatchAPI) calls() []string {
	recorded := f.capture.All()
	calls := make([]string, 0, len(recorded))
	for _, req := range recorded {
		call := req.Method + " " + req.Path
		if query := req.Query.Encode(); query != "" {
			call += "?" + query
		}
		calls = append(calls, call)
	}
	return calls
}

package bedrock

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/enterpilot/gomodel/internal/llmclient"
)

const converseEndpoint = "Converse"

type callObservation struct {
	ctx       context.Context
	startedAt time.Time
	info      llmclient.RequestInfo
	hooks     llmclient.Hooks
}

func (p *Provider) beginCallObservation(ctx context.Context, model string, stream bool) callObservation {
	observation := callObservation{
		ctx:       ctx,
		startedAt: time.Now(),
		info: llmclient.RequestInfo{
			Provider:  providerName,
			Model:     model,
			Operation: llmclient.OperationChat,
			Endpoint:  converseEndpoint,
			Method:    http.MethodPost,
			Stream:    stream,
		},
		hooks: p.hooks,
	}
	if p.hooks.OnRequestStart != nil {
		if derived := p.hooks.OnRequestStart(ctx, observation.info); derived != nil {
			observation.ctx = derived
		}
	}
	return observation
}

func (o callObservation) end(statusCode int, err error) {
	if o.hooks.OnRequestEnd == nil {
		return
	}
	o.hooks.OnRequestEnd(o.ctx, o.responseInfo(statusCode, err))
}

func (o callObservation) firstResponseChunk() {
	if o.hooks.OnStreamFirstChunk == nil {
		return
	}
	o.hooks.OnStreamFirstChunk(o.ctx, o.responseInfo(http.StatusOK, nil))
}

func (o callObservation) streamEmpty(err error) {
	if o.hooks.OnStreamEmpty == nil {
		return
	}
	o.hooks.OnStreamEmpty(o.ctx, o.responseInfo(http.StatusOK, err))
}

func (o callObservation) responseInfo(statusCode int, err error) llmclient.ResponseInfo {
	return llmclient.ResponseInfo{
		Provider:   o.info.Provider,
		Model:      o.info.Model,
		Operation:  o.info.Operation,
		Endpoint:   o.info.Endpoint,
		Method:     o.info.Method,
		StatusCode: statusCode,
		Duration:   time.Since(o.startedAt),
		Stream:     o.info.Stream,
		Error:      err,
	}
}

func observedStream(body io.ReadCloser, observation callObservation) io.ReadCloser {
	return &observedReadCloser{
		ReadCloser:  body,
		onFirstRead: observation.firstResponseChunk,
		onEmpty:     observation.streamEmpty,
	}
}

// observedReadCloser reports the first delivered bytes, or that the stream
// ended before delivering any. Exactly one of the two fires.
type observedReadCloser struct {
	io.ReadCloser
	once        sync.Once
	onFirstRead func()
	onEmpty     func(err error)
}

func (r *observedReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	switch {
	case n > 0:
		r.once.Do(r.onFirstRead)
	case err != nil:
		r.once.Do(func() { r.onEmpty(err) })
	}
	return n, err
}

func statusCodeFromError(err error) int {
	type httpStatus interface{ HTTPStatusCode() int }
	if status, ok := err.(httpStatus); ok {
		return status.HTTPStatusCode()
	}
	return 0
}

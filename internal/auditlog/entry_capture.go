package auditlog

import (
	"net/http"

	"gomodel/internal/core"
)

// PopulateRequestData copies the configured request capture fields into the log entry.
// Streaming handlers call this before creating the detached stream entry so request
// metadata is preserved even though the middleware finishes later.
func PopulateRequestData(entry *LogEntry, req *http.Request, cfg Config) {
	if entry == nil || req == nil {
		return
	}

	data := ensureLogData(entry)

	if cfg.LogHeaders {
		data.RequestHeaders = extractHeaders(req.Header)
	}

	if !cfg.LogBodies {
		return
	}

	snapshot := core.GetRequestSnapshot(req.Context())
	if snapshot == nil {
		return
	}

	switch body := snapshot.CapturedBody(); {
	case snapshot.BodyNotCaptured:
		data.RequestBodyTooBigToHandle = true
	case body != nil:
		captureLoggedRequestBody(entry, body)
	}
}

// PopulateResponseHeaders copies response headers into the log entry when header logging is enabled.
func PopulateResponseHeaders(entry *LogEntry, headers http.Header) {
	if entry == nil || headers == nil {
		return
	}

	data := ensureLogData(entry)
	data.ResponseHeaders = extractHeaders(headers)
}

func ensureLogData(entry *LogEntry) *LogData {
	if entry.Data == nil {
		entry.Data = &LogData{}
	}
	return entry.Data
}

package auditlog

import (
	"bytes"
	encjson "encoding/json"
	"unicode/utf8"

	"github.com/goccy/go-json"
)

// Captured request and response bodies are stored verbatim.
//
// A JSON body is kept as the client's own bytes (json.RawMessage) rather than
// decoded into maps on the request path: decoding is by far the most
// expensive step of audit capture, and every JSON consumer — the SQL stores,
// the live dashboard feed, the admin API — only re-encodes the value anyway.
// Raw bytes also preserve what the maps lost: large-integer precision, key
// order, and the exact spelling of numbers.
//
// Only MongoDB needs a structured document (it indexes fields inside the
// body); its writer converts with BodyDocument, off the request path.

// captureLoggedBody converts raw body bytes into the representation audit
// entries store: the JSON itself when the body is valid JSON, otherwise a
// valid-UTF-8 string. The bytes are copied because the entry outlives the
// request buffers it was captured from.
func captureLoggedBody(bodyBytes []byte) any {
	bodyBytes = bytes.TrimSpace(bodyBytes)
	if len(bodyBytes) == 0 {
		return nil
	}
	// encoding/json's validator is a strict, allocation-free scan; goccy's
	// Valid decodes the document to check it, which is the cost this type
	// exists to avoid. It does not check UTF-8 inside strings, so that is
	// checked separately: stores reject invalid UTF-8 (BSON, Postgres jsonb),
	// and such bodies take the coerced-string fallback exactly as before.
	if utf8.Valid(bodyBytes) && encjson.Valid(bodyBytes) {
		return json.RawMessage(bytes.Clone(bodyBytes))
	}
	return toValidUTF8String(bodyBytes)
}

// CaptureLoggedBody is captureLoggedBody for handlers outside this package
// that capture their own bodies.
func CaptureLoggedBody(bodyBytes []byte) any {
	return captureLoggedBody(bodyBytes)
}

// BodyDocument returns the structured form of a captured body: a JSON body
// is decoded into maps, slices and scalars; every other value (a decoded
// document read back from a store, an audio/image capture struct, a string
// fallback) is returned unchanged. Undecodable JSON, which capture never
// produces, is returned as-is so nothing is silently dropped.
func BodyDocument(body any) any {
	raw, ok := body.(json.RawMessage)
	if !ok {
		return body
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return body
	}
	return document
}

// withBodyDocuments returns a copy of the entry whose captured JSON bodies
// (request, response, per-attempt responses, and per-revision bodies) are
// decoded documents, for stores that need structure. The receiver is left
// untouched.
func (e *LogEntry) withBodyDocuments() *LogEntry {
	if e == nil || e.Data == nil {
		return e
	}
	entry := *e
	data := *e.Data
	data.RequestBody = BodyDocument(data.RequestBody)
	data.ResponseBody = BodyDocument(data.ResponseBody)
	if len(data.Attempts) > 0 {
		attempts := make([]AttemptSnapshot, len(data.Attempts))
		for i, attempt := range data.Attempts {
			attempt.ResponseBody = BodyDocument(attempt.ResponseBody)
			attempts[i] = attempt
		}
		data.Attempts = attempts
	}
	if len(data.RequestRevisions) > 0 {
		revisions := make([]RequestRevisionSnapshot, len(data.RequestRevisions))
		for i, revision := range data.RequestRevisions {
			revision.Body = BodyDocument(revision.Body)
			revisions[i] = revision
		}
		data.RequestRevisions = revisions
	}
	entry.Data = &data
	return &entry
}

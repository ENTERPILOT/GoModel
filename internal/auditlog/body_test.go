package auditlog

import (
	"bytes"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCaptureLoggedBody(t *testing.T) {
	t.Run("json is kept verbatim as raw bytes", func(t *testing.T) {
		src := []byte(` {"b": 1, "a": [1, 2], "big": 9007199254740993} `)
		got, ok := captureLoggedBody(src).(json.RawMessage)
		require.True(t, ok, "captured = %T, want json.RawMessage", captureLoggedBody(src))
		want := bytes.TrimSpace(src)
		require.Equal(t, want, []byte(got))

		// The entry outlives the request buffer, so the capture must own its bytes.
		src[3] = 'x'
		require.False(t, bytes.Contains(got, []byte("x")))
	})

	t.Run("scalars and arrays are json too", func(t *testing.T) {
		for _, raw := range []string{`"text"`, `42`, `null`, `[1,2]`} {
			_, ok := captureLoggedBody([]byte(raw)).(json.RawMessage)
			assert.True(t, ok, "%s: captured as %T, want json.RawMessage", raw, captureLoggedBody([]byte(raw)))
		}
	})

	t.Run("invalid json falls back to a valid utf-8 string", func(t *testing.T) {
		require.Equal(t, "upstream is down", captureLoggedBody([]byte("upstream is down")))
		require.Equal(t, "{\"a\":\"x\x01y\"}", captureLoggedBody([]byte("{\"a\":\"x\x01y\"}")))
		require.Equal(t, "bad � utf8", captureLoggedBody([]byte("bad \xff utf8")))
		// encoding/json.Valid accepts invalid UTF-8 inside strings; stores do not.
		require.Equal(t, "{\"text\":\"�\"}", captureLoggedBody([]byte("{\"text\":\"\xff\"}")))
	})

	t.Run("empty is nil", func(t *testing.T) {
		require.Nil(t, captureLoggedBody(nil))
		require.Nil(t, captureLoggedBody([]byte("  \n")))
	})
}

func TestBodyDocument(t *testing.T) {
	doc, ok := BodyDocument(json.RawMessage(`{"id":"resp_1","n":[1,2]}`)).(map[string]any)
	require.True(t, ok)
	require.Equal(t, "resp_1", doc["id"], "raw json decoded to %#v", BodyDocument(json.RawMessage(`{"id":"resp_1","n":[1,2]}`)))

	for _, passthrough := range []any{nil, "text", map[string]any{"already": "decoded"}, AudioBodyLog{ContentType: "audio/mpeg"}} {
		assert.Equal(t, passthrough, BodyDocument(passthrough))
	}
	got := BodyDocument(json.RawMessage(`{bad`))
	require.Equal(t, `{bad`, string(got.(json.RawMessage)))
}

func TestWithBodyDocumentsDecodesForDocumentStores(t *testing.T) {
	entry := &LogEntry{
		ID: "audit-1",
		Data: &LogData{
			RequestBody:  json.RawMessage(`{"previous_response_id":"resp_0"}`),
			ResponseBody: json.RawMessage(`{"id":"resp_1"}`),
			Attempts: []AttemptSnapshot{
				{Seq: 1, ResponseBody: json.RawMessage(`{"error":{"code":"overloaded"}}`)},
				{Seq: 2, ResponseBody: "plain text"},
			},
			RequestRevisions: []RequestRevisionSnapshot{
				{Seq: 1, Rewriter: "compress", Body: json.RawMessage(`{"messages":[]}`)},
			},
		},
	}

	doc := entry.withBodyDocuments()

	revision, ok := doc.Data.RequestRevisions[0].Body.(map[string]any)
	require.True(t, ok)
	require.NotNil(t, revision["messages"], "revision body = %#v, want decoded document", doc.Data.RequestRevisions[0].Body)
	_, ok = entry.Data.RequestRevisions[0].Body.(json.RawMessage)
	require.True(t, ok, "receiver revision body mutated to %T", entry.Data.RequestRevisions[0].Body)

	req, ok := doc.Data.RequestBody.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "resp_0", req["previous_response_id"])

	resp, ok := doc.Data.ResponseBody.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "resp_1", resp["id"])

	attempt, ok := doc.Data.Attempts[0].ResponseBody.(map[string]any)
	require.True(t, ok)
	require.NotNil(t, attempt["error"], "attempt body = %#v, want decoded document", doc.Data.Attempts[0].ResponseBody)
	require.Equal(t, "plain text", doc.Data.Attempts[1].ResponseBody)
	// The original entry is what other stores and the live feed still hold.
	_, ok = entry.Data.RequestBody.(json.RawMessage)
	require.True(t, ok, "receiver request body mutated to %T", entry.Data.RequestBody)
	_, ok = entry.Data.Attempts[0].ResponseBody.(json.RawMessage)
	require.True(t, ok, "receiver attempt body mutated to %T", entry.Data.Attempts[0].ResponseBody)
	require.Nil(t, (*LogEntry)(nil).withBodyDocuments())
	got := (&LogEntry{ID: "no-data"}).withBodyDocuments()
	require.Nil(t, got.Data)
}

func TestExtractStringFieldReadsRawBodies(t *testing.T) {
	entry := &LogEntry{Data: &LogData{
		RequestBody:  json.RawMessage(`{"previous_response_id":" resp_0 "}`),
		ResponseBody: json.RawMessage(`{"id":"resp_1"}`),
	}}
	require.Equal(t, "resp_0", extractPreviousResponseID(entry))
	require.Equal(t, "resp_1", extractResponseID(entry))
}

func TestMarshalLogDataEmbedsRawBodiesAsJSON(t *testing.T) {
	data := &LogData{
		RequestBody:  json.RawMessage(`{ "model" : "gpt-4o", "big": 9007199254740993 }`),
		ResponseBody: "not json",
	}
	out := marshalLogData(data, "audit-1")

	var decoded struct {
		RequestBody  map[string]any `json:"request_body"`
		ResponseBody string         `json:"response_body"`
	}
	err := json.Unmarshal(out, &decoded)
	require.NoError(t, err, "stored data is not JSON: %v\n%s", err, out)
	require.Equal(t, "gpt-4o", decoded.RequestBody["model"])
	require.Equal(t, "not json", decoded.ResponseBody, "stored data = %s", out)
	require.Contains(t, string(out), string([]byte(`9007199254740993`)))
}

package auditlog

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/goccy/go-json"
)

func TestCaptureLoggedBody(t *testing.T) {
	t.Run("json is kept verbatim as raw bytes", func(t *testing.T) {
		src := []byte(` {"b": 1, "a": [1, 2], "big": 9007199254740993} `)
		got, ok := captureLoggedBody(src).(json.RawMessage)
		if !ok {
			t.Fatalf("captured = %T, want json.RawMessage", captureLoggedBody(src))
		}
		if want := bytes.TrimSpace(src); !bytes.Equal(got, want) {
			t.Fatalf("captured = %s, want %s", got, want)
		}
		// The entry outlives the request buffer, so the capture must own its bytes.
		src[3] = 'x'
		if bytes.Contains(got, []byte("x")) {
			t.Fatal("captured body aliases the request buffer")
		}
	})

	t.Run("scalars and arrays are json too", func(t *testing.T) {
		for _, raw := range []string{`"text"`, `42`, `null`, `[1,2]`} {
			if _, ok := captureLoggedBody([]byte(raw)).(json.RawMessage); !ok {
				t.Errorf("%s: captured as %T, want json.RawMessage", raw, captureLoggedBody([]byte(raw)))
			}
		}
	})

	t.Run("invalid json falls back to a valid utf-8 string", func(t *testing.T) {
		if got := captureLoggedBody([]byte("upstream is down")); got != "upstream is down" {
			t.Fatalf("plain text = %#v", got)
		}
		if got := captureLoggedBody([]byte("{\"a\":\"x\x01y\"}")); got != "{\"a\":\"x\x01y\"}" {
			t.Fatalf("control character body = %#v, want string fallback", got)
		}
		if got := captureLoggedBody([]byte("bad \xff utf8")); got != "bad � utf8" {
			t.Fatalf("invalid utf-8 = %#v", got)
		}
	})

	t.Run("empty is nil", func(t *testing.T) {
		if got := captureLoggedBody(nil); got != nil {
			t.Fatalf("nil body = %#v", got)
		}
		if got := captureLoggedBody([]byte("  \n")); got != nil {
			t.Fatalf("whitespace body = %#v", got)
		}
	})
}

func TestBodyDocument(t *testing.T) {
	doc, ok := BodyDocument(json.RawMessage(`{"id":"resp_1","n":[1,2]}`)).(map[string]any)
	if !ok || doc["id"] != "resp_1" {
		t.Fatalf("raw json decoded to %#v", BodyDocument(json.RawMessage(`{"id":"resp_1","n":[1,2]}`)))
	}
	for _, passthrough := range []any{nil, "text", map[string]any{"already": "decoded"}, AudioBodyLog{ContentType: "audio/mpeg"}} {
		if got := BodyDocument(passthrough); !reflect.DeepEqual(got, passthrough) {
			t.Errorf("BodyDocument(%#v) = %#v, want unchanged", passthrough, got)
		}
	}
	if got := BodyDocument(json.RawMessage(`{bad`)); string(got.(json.RawMessage)) != `{bad` {
		t.Fatalf("undecodable raw = %#v, want unchanged", got)
	}
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
		},
	}

	doc := entry.withBodyDocuments()

	req, ok := doc.Data.RequestBody.(map[string]any)
	if !ok || req["previous_response_id"] != "resp_0" {
		t.Fatalf("request body = %#v, want decoded document", doc.Data.RequestBody)
	}
	resp, ok := doc.Data.ResponseBody.(map[string]any)
	if !ok || resp["id"] != "resp_1" {
		t.Fatalf("response body = %#v, want decoded document", doc.Data.ResponseBody)
	}
	attempt, ok := doc.Data.Attempts[0].ResponseBody.(map[string]any)
	if !ok || attempt["error"] == nil {
		t.Fatalf("attempt body = %#v, want decoded document", doc.Data.Attempts[0].ResponseBody)
	}
	if doc.Data.Attempts[1].ResponseBody != "plain text" {
		t.Fatalf("non-json attempt body = %#v, want unchanged", doc.Data.Attempts[1].ResponseBody)
	}

	// The original entry is what other stores and the live feed still hold.
	if _, ok := entry.Data.RequestBody.(json.RawMessage); !ok {
		t.Fatalf("receiver request body mutated to %T", entry.Data.RequestBody)
	}
	if _, ok := entry.Data.Attempts[0].ResponseBody.(json.RawMessage); !ok {
		t.Fatalf("receiver attempt body mutated to %T", entry.Data.Attempts[0].ResponseBody)
	}

	if (*LogEntry)(nil).withBodyDocuments() != nil {
		t.Fatal("nil entry must stay nil")
	}
	if got := (&LogEntry{ID: "no-data"}).withBodyDocuments(); got.Data != nil {
		t.Fatal("entry without data must keep nil data")
	}
}

func TestExtractStringFieldReadsRawBodies(t *testing.T) {
	entry := &LogEntry{Data: &LogData{
		RequestBody:  json.RawMessage(`{"previous_response_id":" resp_0 "}`),
		ResponseBody: json.RawMessage(`{"id":"resp_1"}`),
	}}
	if got := extractPreviousResponseID(entry); got != "resp_0" {
		t.Fatalf("previous response id = %q, want resp_0", got)
	}
	if got := extractResponseID(entry); got != "resp_1" {
		t.Fatalf("response id = %q, want resp_1", got)
	}
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
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("stored data is not JSON: %v\n%s", err, out)
	}
	if decoded.RequestBody["model"] != "gpt-4o" || decoded.ResponseBody != "not json" {
		t.Fatalf("stored data = %s", out)
	}
	if !bytes.Contains(out, []byte(`9007199254740993`)) {
		t.Fatalf("large integer lost precision: %s", out)
	}
}

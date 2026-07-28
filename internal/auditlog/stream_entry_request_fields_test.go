package auditlog

import (
	"context"
	"reflect"
	"testing"
)

// responseSideLogDataFields are the LogData fields CreateStreamEntry may
// legitimately leave unset: they are not known when the stream entry is
// created and are filled in by the stream observer once the stream closes.
// Every other field describes the request and must survive the copy.
var responseSideLogDataFields = map[string]bool{
	"ResponseBody":               true,
	"ResponseBodyTooBigToHandle": true,
	"ErrorMessage":               true,
	"ErrorCode":                  true,
}

// A streamed request is persisted from the CreateStreamEntry copy — the base
// entry is never written — so an ingress rewrite chain recorded by
// EnrichEntryWithRequestRevision is lost unless the copy carries it. This
// regression covers request rewriters (e.g. pro token compression) whose
// "Rewritten" audit pane vanished on every successful streamed request while
// surviving on the non-streamed error path.
func TestCreateStreamEntryPreservesRequestRevisions(t *testing.T) {
	base := &LogEntry{
		ID:   "entry-1",
		Path: "/v1/chat/completions",
		Data: &LogData{
			RequestRevisions: []RequestRevisionSnapshot{{
				Seq:         1,
				Rewriter:    "pro-token-compression",
				BytesBefore: 65209,
				BytesAfter:  64418,
				TokensSaved: 189,
				Detail:      map[string]any{"chars_removed": 757},
			}},
			RequestBodyTooBigToHandle: true,
		},
	}

	streamEntry := CreateStreamEntry(context.Background(), base)
	if streamEntry == nil || streamEntry.Data == nil {
		t.Fatal("expected a stream entry with data")
	}

	got := streamEntry.Data.RequestRevisions
	if len(got) != 1 {
		t.Fatalf("RequestRevisions dropped: got %d revisions, want 1", len(got))
	}
	if got[0].Rewriter != "pro-token-compression" || got[0].TokensSaved != 189 {
		t.Fatalf("revision not copied faithfully: %+v", got[0])
	}
	if got[0].BytesBefore != 65209 || got[0].BytesAfter != 64418 {
		t.Fatalf("revision byte counts not copied: %+v", got[0])
	}
	if !streamEntry.Data.RequestBodyTooBigToHandle {
		t.Error("RequestBodyTooBigToHandle dropped")
	}

	// The copy must own its slice. Appending to the base entry would not show
	// that: the source literal has no spare capacity, so append reallocates and
	// leaves the copy alone whether or not the two share an array. Writing
	// through an existing element is what actually distinguishes them.
	base.Data.RequestRevisions[0].Rewriter = "mutated"
	if streamEntry.Data.RequestRevisions[0].Rewriter != "pro-token-compression" {
		t.Error("stream entry shares its revision backing array with the base entry")
	}
}

// The nil case is the one the stream observer sees most often — most requests
// carry no rewriter — and it must stay nil rather than becoming an empty slice,
// so a streamed entry without rewrites serializes the same as it always did.
func TestCreateStreamEntryLeavesAbsentRequestRevisionsNil(t *testing.T) {
	streamEntry := CreateStreamEntry(context.Background(), &LogEntry{ID: "entry-1", Data: &LogData{UserAgent: "curl/8"}})
	if streamEntry == nil || streamEntry.Data == nil {
		t.Fatal("expected a stream entry with data")
	}
	if streamEntry.Data.RequestRevisions != nil {
		t.Errorf("RequestRevisions = %#v, want nil", streamEntry.Data.RequestRevisions)
	}
	if streamEntry.Data.UserAgent != "curl/8" {
		t.Errorf("UserAgent = %q, want %q", streamEntry.Data.UserAgent, "curl/8")
	}
}

// CreateStreamEntry builds LogData with a field whitelist, so any request-side
// field added to LogData later is silently dropped until someone remembers to
// extend that literal. This walks LogData by reflection and fails when a
// request-side field does not survive, which is how RequestRevisions went
// missing in the first place.
func TestCreateStreamEntryCopiesEveryRequestSideField(t *testing.T) {
	populated := &LogData{}
	v := reflect.ValueOf(populated).Elem()
	typ := v.Type()

	for i := range typ.NumField() {
		field := typ.Field(i)
		if !v.Field(i).CanSet() {
			continue
		}
		if !setRecognizableValue(v.Field(i)) {
			t.Fatalf("test needs a sample value for LogData.%s (%s)", field.Name, field.Type)
		}
	}

	streamEntry := CreateStreamEntry(context.Background(), &LogEntry{ID: "entry-1", Data: populated})
	if streamEntry == nil || streamEntry.Data == nil {
		t.Fatal("expected a stream entry with data")
	}

	copied := reflect.ValueOf(streamEntry.Data).Elem()
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if responseSideLogDataFields[name] {
			continue
		}
		if copied.Field(i).IsZero() {
			t.Errorf("LogData.%s is a request-side field but CreateStreamEntry dropped it", name)
		}
	}
}

// setRecognizableValue fills one field with a non-zero value so a dropped
// field shows up as the zero value on the other side of the copy.
func setRecognizableValue(field reflect.Value) bool {
	switch field.Kind() {
	case reflect.String:
		field.SetString("x")
	case reflect.Bool:
		field.SetBool(true)
	case reflect.Map:
		m := reflect.MakeMap(field.Type())
		m.SetMapIndex(reflect.ValueOf("k"), reflect.ValueOf("v"))
		field.Set(m)
	case reflect.Slice:
		field.Set(reflect.MakeSlice(field.Type(), 1, 1))
	case reflect.Pointer:
		field.Set(reflect.New(field.Type().Elem()))
	case reflect.Interface:
		field.Set(reflect.ValueOf(map[string]any{"k": "v"}))
	default:
		return false
	}
	return true
}

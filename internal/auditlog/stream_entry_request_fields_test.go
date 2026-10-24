package auditlog

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	"ErrorProvider":              true,
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
	require.NotNil(t, streamEntry)
	require.NotNil(t, streamEntry.Data)

	got := streamEntry.Data.RequestRevisions
	require.Len(t, got, 1)
	require.Equal(t, "pro-token-compression", got[0].Rewriter)
	require.Equal(t, 189, got[0].TokensSaved, "revision not copied faithfully: %+v", got[0])
	require.Equal(t, 65209, got[0].BytesBefore)
	require.Equal(t, 64418, got[0].BytesAfter, "revision byte counts not copied: %+v", got[0])
	assert.True(t, streamEntry.Data.RequestBodyTooBigToHandle)

	// The copy must own its slice. Appending to the base entry would not show
	// that: the source literal has no spare capacity, so append reallocates and
	// leaves the copy alone whether or not the two share an array. Writing
	// through an existing element is what actually distinguishes them.
	base.Data.RequestRevisions[0].Rewriter = "mutated"
	assert.Equal(t, "pro-token-compression", streamEntry.Data.RequestRevisions[0].Rewriter)
}

// The nil case is the one the stream observer sees most often — most requests
// carry no rewriter — and it must stay nil rather than becoming an empty slice,
// so a streamed entry without rewrites serializes the same as it always did.
func TestCreateStreamEntryLeavesAbsentRequestRevisionsNil(t *testing.T) {
	streamEntry := CreateStreamEntry(context.Background(), &LogEntry{ID: "entry-1", Data: &LogData{UserAgent: "curl/8"}})
	require.NotNil(t, streamEntry)
	require.NotNil(t, streamEntry.Data)
	assert.Nil(t, streamEntry.Data.RequestRevisions)
	assert.Equal(t, "curl/8", streamEntry.Data.UserAgent)
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
		require.True(t, setRecognizableValue(v.Field(i)), "test needs a sample value for LogData.%s (%s)", field.Name, field.Type)
	}

	streamEntry := CreateStreamEntry(context.Background(), &LogEntry{ID: "entry-1", Data: populated})
	require.NotNil(t, streamEntry)
	require.NotNil(t, streamEntry.Data)

	copied := reflect.ValueOf(streamEntry.Data).Elem()
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if responseSideLogDataFields[name] {
			continue
		}
		assert.False(t, copied.Field(i).IsZero(), "LogData.%s is a request-side field but CreateStreamEntry dropped it", name)
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

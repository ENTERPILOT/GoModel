package core

import (
	"reflect"

	"github.com/goccy/go-json"
	"slices"
	"testing"
)

// The utility request types must accept exactly the ResponsesRequest field set
// minus the streaming controls (utility endpoints never stream). This pins the
// contract so adding a field to one struct without the other fails here
// instead of silently dropping the field at the other endpoint.
func TestResponsesUtilityFieldParity(t *testing.T) {
	full := jsonFieldNames(ResponsesRequest{})
	utility := jsonFieldNames(ResponseInputTokensRequest{})

	want := make([]string, 0, len(full))
	for _, name := range full {
		if name == "stream" || name == "stream_options" {
			continue
		}
		want = append(want, name)
	}
	slices.Sort(want)
	got := slices.Clone(utility)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("ResponseInputTokensRequest fields = %v, want ResponsesRequest minus stream controls = %v", got, want)
	}
}

// InputTokensRequest must copy every field the utility type declares. Filled
// via reflection so a newly added field that the reduction forgets to copy
// fails this test rather than silently arriving zero-valued upstream.
func TestInputTokensRequestCopiesEveryField(t *testing.T) {
	var full ResponsesRequest
	fill(t, reflect.ValueOf(&full).Elem())
	full.ExtraFields = UnknownJSONFieldsFromMap(map[string]json.RawMessage{"x_custom": json.RawMessage(`1`)})

	reduced := full.InputTokensRequest()
	if reduced == nil {
		t.Fatal("InputTokensRequest() = nil")
	}

	rv := reflect.ValueOf(*reduced)
	rt := rv.Type()
	for i := range rt.NumField() {
		field := rt.Field(i)
		if field.Name == "ExtraFields" {
			if reduced.ExtraFields.IsEmpty() {
				t.Fatal("ExtraFields were not cloned")
			}
			continue
		}
		if rv.Field(i).IsZero() {
			t.Fatalf("InputTokensRequest() left %s zero-valued; the reduction must copy it", field.Name)
		}
	}

	if compact := full.CompactRequest(); compact == nil || compact.Model != full.Model {
		t.Fatalf("CompactRequest() = %+v, want model %q", compact, full.Model)
	}
	var nilReq *ResponsesRequest
	if nilReq.InputTokensRequest() != nil || nilReq.CompactRequest() != nil {
		t.Fatal("nil receiver must reduce to nil")
	}
}

// fill sets every exported field of v to a non-zero value.
func fill(t *testing.T, v reflect.Value) {
	t.Helper()
	for _, field := range v.Fields() {
		if !field.CanSet() {
			continue
		}
		switch field.Kind() {
		case reflect.String:
			field.SetString("x")
		case reflect.Bool:
			field.SetBool(true)
		case reflect.Pointer:
			field.Set(reflect.New(field.Type().Elem()))
			switch elem := field.Elem(); elem.Kind() {
			case reflect.Float64:
				elem.SetFloat(1)
			case reflect.Int:
				elem.SetInt(1)
			case reflect.Bool:
				elem.SetBool(true)
			case reflect.Struct:
				fill(t, elem)
			}
		case reflect.Slice:
			field.Set(reflect.MakeSlice(field.Type(), 1, 1))
		case reflect.Map:
			field.Set(reflect.MakeMapWithSize(field.Type(), 1))
			field.SetMapIndex(reflect.ValueOf("k"), reflect.ValueOf("v"))
		case reflect.Interface:
			field.Set(reflect.ValueOf(any("x")))
		case reflect.Struct:
			fill(t, field)
		}
	}
}

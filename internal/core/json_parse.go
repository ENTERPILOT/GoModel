package core

import (
	"unsafe"

	"github.com/tidwall/gjson"
)

// ParseJSONNoCopy is gjson.ParseBytes without its copy of the whole input:
// ParseBytes converts b to a string, which on a request body costs one
// body-sized allocation per call.
//
// The result reads b in place, so it and every value derived from it (Raw,
// Str, String(), keys) alias b. Callers must not let any of them outlive b or
// be used after b is modified, and must copy out (strings.Clone, bytes.Clone,
// or writing into their own buffer) any string they retain.
func ParseJSONNoCopy(b []byte) gjson.Result {
	return gjson.Parse(unsafe.String(unsafe.SliceData(b), len(b)))
}

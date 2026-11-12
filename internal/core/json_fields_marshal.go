package core

import (
	"bytes"
	"sync"

	"github.com/goccy/go-json"
)

// mergingEncoder encodes one object level and merges its unknown members in
// the same step. json.Marshal copies the encoder's pooled buffer into a new
// slice, and merging the extras into that slice would copy the level a second
// time. Writing through an Encoder instead hands the pooled buffer to Write,
// which builds the merged object straight from it in one exact-size copy.
// Encoder applies the same options as json.Marshal (HTML escaping, UTF-8
// normalization), so the bytes are identical.
type mergingEncoder struct {
	enc    *json.Encoder
	extras []byte
	out    []byte
	err    error
}

var mergingEncoderPool = sync.Pool{New: func() any {
	m := &mergingEncoder{}
	m.enc = json.NewEncoder(m)
	return m
}}

// Write receives the encoded base object followed by Encode's newline. p
// aliases the encoder's pooled buffer; mergeUnknownJSONObject always returns a
// fresh slice, so the result never points into the pool.
func (m *mergingEncoder) Write(p []byte) (int, error) {
	m.out, m.err = mergeUnknownJSONObject(bytes.TrimSuffix(p, []byte{'\n'}), m.extras)
	return len(p), nil
}

// marshalMergingUnknownJSONObject returns the JSON encoding of base with the
// members of extraBody appended, as mergeUnknownJSONObject(json.Marshal(base),
// extraBody) would, but with one allocation for the result instead of two.
func marshalMergingUnknownJSONObject(base any, extraBody []byte) ([]byte, error) {
	m := mergingEncoderPool.Get().(*mergingEncoder)
	m.extras = extraBody
	err := m.enc.Encode(base)
	out, mergeErr := m.out, m.err
	m.extras, m.out, m.err = nil, nil, nil
	mergingEncoderPool.Put(m)
	if err != nil {
		return nil, err
	}
	return out, mergeErr
}

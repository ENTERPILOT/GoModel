package core

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"math"
	"strings"

	"github.com/goccy/go-json"
)

// maxEmbeddingDims caps how large a single vector may be before encoding
// conversion is skipped. It sits far above any real embedding model (the
// largest in common use are a few thousand dimensions) and exists only to bound
// allocations and prevent the *4 byte-size computation from overflowing.
const maxEmbeddingDims = 1 << 20

// NormalizeEmbeddingEncoding reconciles a response's embedding encoding with the
// encoding_format the client requested, keeping responses OpenAI-compatible
// regardless of provider quirks.
//
// The OpenAI Python and JS/LangChain SDKs request encoding_format="base64" by
// default and decode it client-side. Some OpenAI-compatible servers (notably
// LM Studio) ignore encoding_format and always return float arrays, which makes
// those SDKs mis-decode the floats as packed bytes and produce corrupted,
// wrong-dimension vectors. Following Postel's Law, GoModel accepts whatever the
// upstream returns and re-encodes each vector into the format the caller asked
// for: base64 (little-endian float32, matching OpenAI) or a float array.
//
// An empty or unrecognized format is treated as "float" (the OpenAI default
// when the field is omitted). Vectors already in the requested form, and values
// that don't parse as either shape, are left untouched.
func NormalizeEmbeddingEncoding(resp *EmbeddingResponse, encodingFormat string) {
	if resp == nil {
		return
	}
	wantBase64 := strings.EqualFold(strings.TrimSpace(encodingFormat), "base64")
	for i := range resp.Data {
		raw := bytes.TrimSpace(resp.Data[i].Embedding)
		if len(raw) == 0 {
			continue
		}
		if wantBase64 {
			if encoded, ok := floatArrayToBase64(raw); ok {
				resp.Data[i].Embedding = encoded
			}
			continue
		}
		if decoded, ok := base64ToFloatArray(raw); ok {
			resp.Data[i].Embedding = decoded
		}
	}
}

// floatArrayToBase64 converts a JSON float array into an OpenAI-style base64
// string (little-endian float32). It returns ok=false when raw is not a JSON
// array (e.g. already a base64 string), so the caller leaves it as-is.
func floatArrayToBase64(raw json.RawMessage) (json.RawMessage, bool) {
	if len(raw) == 0 || raw[0] != '[' {
		return nil, false
	}
	var values []float64
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, false
	}
	// Guard the *4 buffer sizing against overflow / absurd payloads. Real
	// embedding vectors are at most a few thousand dimensions; anything beyond
	// the cap is left untouched rather than allocated.
	if len(values) > maxEmbeddingDims {
		return nil, false
	}
	buf := make([]byte, len(values)*4)
	for i, v := range values {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(float32(v)))
	}
	encoded, err := json.Marshal(base64.StdEncoding.EncodeToString(buf))
	if err != nil {
		return nil, false
	}
	return encoded, true
}

// base64ToFloatArray converts an OpenAI-style base64 string (little-endian
// float32) into a JSON float array. It returns ok=false when raw is not a JSON
// string or doesn't decode to whole float32 values, so the caller leaves it
// as-is (e.g. it's already a float array).
func base64ToFloatArray(raw json.RawMessage) (json.RawMessage, bool) {
	if len(raw) == 0 || raw[0] != '"' {
		return nil, false
	}
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return nil, false
	}
	// Reject oversized payloads before DecodeString allocates the decode buffer,
	// not just after, so a pathological upstream can't force a large allocation.
	if len(encoded) > base64.StdEncoding.EncodedLen(maxEmbeddingDims*4) {
		return nil, false
	}
	buf, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(buf) == 0 || len(buf)%4 != 0 || len(buf)/4 > maxEmbeddingDims {
		return nil, false
	}
	values := make([]float64, len(buf)/4)
	for i := range values {
		values[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:])))
	}
	decoded, err := json.Marshal(values)
	if err != nil {
		return nil, false
	}
	return decoded, true
}

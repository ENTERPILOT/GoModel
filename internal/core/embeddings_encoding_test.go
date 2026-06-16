package core

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"testing"
)

func b64OfFloats(vals []float32) string {
	buf := make([]byte, len(vals)*4)
	for i, v := range vals {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return base64.StdEncoding.EncodeToString(buf)
}

func TestNormalizeEmbeddingEncoding(t *testing.T) {
	tests := []struct {
		name   string
		format string
		in     json.RawMessage
		want   json.RawMessage
	}{
		{
			name:   "float array to base64 when base64 requested",
			format: "base64",
			in:     json.RawMessage(`[1,2,3]`),
			want:   json.RawMessage(`"` + b64OfFloats([]float32{1, 2, 3}) + `"`),
		},
		{
			name:   "base64 to float array when float requested",
			format: "float",
			in:     json.RawMessage(`"` + b64OfFloats([]float32{1, 2, 3}) + `"`),
			want:   json.RawMessage(`[1,2,3]`),
		},
		{
			name:   "base64 to float array when format omitted (OpenAI default)",
			format: "",
			in:     json.RawMessage(`"` + b64OfFloats([]float32{0.5, -0.25}) + `"`),
			want:   json.RawMessage(`[0.5,-0.25]`),
		},
		{
			name:   "already base64 left unchanged when base64 requested",
			format: "base64",
			in:     json.RawMessage(`"` + b64OfFloats([]float32{1, 2}) + `"`),
			want:   json.RawMessage(`"` + b64OfFloats([]float32{1, 2}) + `"`),
		},
		{
			name:   "already float left unchanged when float requested",
			format: "float",
			in:     json.RawMessage(`[1,2,3]`),
			want:   json.RawMessage(`[1,2,3]`),
		},
		{
			name:   "case-insensitive format",
			format: "BASE64",
			in:     json.RawMessage(`[1]`),
			want:   json.RawMessage(`"` + b64OfFloats([]float32{1}) + `"`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &EmbeddingResponse{Data: []EmbeddingData{{Embedding: tt.in}}}
			NormalizeEmbeddingEncoding(resp, tt.format)
			if got := string(resp.Data[0].Embedding); got != string(tt.want) {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}

// TestNormalizeEmbeddingEncoding_RoundTrip guards that float->base64->float is
// lossless at float32 precision — the property LangChain/OpenAI clients rely on.
func TestNormalizeEmbeddingEncoding_RoundTrip(t *testing.T) {
	orig := []float32{0.123, -0.987, 1.5, 0, 42.25}
	floats := make([]any, len(orig))
	for i, v := range orig {
		floats[i] = v
	}
	rawFloats, _ := json.Marshal(floats)

	resp := &EmbeddingResponse{Data: []EmbeddingData{{Embedding: rawFloats}}}
	NormalizeEmbeddingEncoding(resp, "base64") // float -> base64
	if resp.Data[0].Embedding[0] != '"' {
		t.Fatalf("expected base64 string, got %s", resp.Data[0].Embedding)
	}
	NormalizeEmbeddingEncoding(resp, "float") // base64 -> float

	var back []float32
	if err := json.Unmarshal(resp.Data[0].Embedding, &back); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(back) != len(orig) {
		t.Fatalf("len = %d, want %d", len(back), len(orig))
	}
	for i := range orig {
		if back[i] != orig[i] {
			t.Errorf("index %d: got %v, want %v", i, back[i], orig[i])
		}
	}
}

// TestNormalizeEmbeddingEncoding_Malformed ensures non-vector payloads are left
// untouched rather than corrupted or panicking.
func TestNormalizeEmbeddingEncoding_Malformed(t *testing.T) {
	for _, in := range []string{`null`, `"not-base64!!!"`, `{}`, `[`, ``} {
		resp := &EmbeddingResponse{Data: []EmbeddingData{{Embedding: json.RawMessage(in)}}}
		NormalizeEmbeddingEncoding(resp, "base64")
		if got := string(resp.Data[0].Embedding); got != in {
			t.Errorf("input %q mutated to %q", in, got)
		}
	}
}

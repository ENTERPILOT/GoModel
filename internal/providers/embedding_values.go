package providers

import (
	"encoding/base64"
	"encoding/binary"
	"math"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// EmbeddingInputs normalizes an OpenAI embeddings input value into a list of
// strings for providers whose native APIs embed text only. Token-array inputs
// are rejected because they cannot be translated faithfully.
func EmbeddingInputs(input any) ([]string, error) {
	switch v := input.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, core.NewInvalidRequestError("embedding input is required", nil)
		}
		return []string{v}, nil
	case []string:
		return nonEmptyEmbeddingInputs(v)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			text, ok := item.(string)
			if !ok {
				return nil, core.NewInvalidRequestError("embeddings support string inputs", nil)
			}
			out = append(out, text)
		}
		return nonEmptyEmbeddingInputs(out)
	default:
		return nil, core.NewInvalidRequestError("embeddings support string inputs", nil)
	}
}

func nonEmptyEmbeddingInputs(inputs []string) ([]string, error) {
	for _, input := range inputs {
		if strings.TrimSpace(input) == "" {
			return nil, core.NewInvalidRequestError("embedding input must not be empty", nil)
		}
	}
	if len(inputs) == 0 {
		return nil, core.NewInvalidRequestError("embedding input is required", nil)
	}
	return inputs, nil
}

// EncodeEmbeddingValues renders one embedding vector the way the OpenAI API
// does: a JSON float array by default, or a base64-encoded little-endian
// float32 buffer when the request asked for encoding_format "base64".
func EncodeEmbeddingValues(values []float64, encodingFormat string) (json.RawMessage, error) {
	if strings.EqualFold(strings.TrimSpace(encodingFormat), "base64") {
		buf := make([]byte, len(values)*4)
		for i, value := range values {
			binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(float32(value)))
		}
		return json.Marshal(base64.StdEncoding.EncodeToString(buf))
	}
	return json.Marshal(values)
}

package server

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/goccy/go-json"
)

// rawJSONObject keeps values encoded until a known field needs to be read or
// changed. This is important for forward-compatible API objects: decoding an
// unknown JSON number through any would round large integers through float64.
type rawJSONObject map[string]json.RawMessage

func decodeRawJSONObject(raw json.RawMessage) (rawJSONObject, error) {
	var object rawJSONObject
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, fmt.Errorf("expected JSON object")
	}
	return object, nil
}

func rawJSONString(object rawJSONObject, key string) string {
	var value string
	if err := json.Unmarshal(object[key], &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func setRawJSONValue(object rawJSONObject, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	object[key] = raw
	return nil
}

func rawJSONValuePresent(object rawJSONObject, key string) bool {
	raw, exists := object[key]
	return exists && len(bytes.TrimSpace(raw)) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

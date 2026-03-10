package core

import "encoding/json"

func cloneRawJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func cloneRawJSONMap(fields map[string]json.RawMessage) map[string]json.RawMessage {
	if len(fields) == 0 {
		return nil
	}
	cloned := make(map[string]json.RawMessage, len(fields))
	for key, value := range fields {
		cloned[key] = cloneRawJSON(value)
	}
	return cloned
}

func extractUnknownJSONFields(data []byte, knownFields ...string) (map[string]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	for _, field := range knownFields {
		delete(raw, field)
	}
	return cloneRawJSONMap(raw), nil
}

func marshalWithUnknownJSONFields(base any, extraFields map[string]json.RawMessage) ([]byte, error) {
	baseBody, err := json.Marshal(base)
	if err != nil {
		return nil, err
	}
	if len(extraFields) == 0 {
		return baseBody, nil
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(baseBody, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		raw = make(map[string]json.RawMessage)
	}
	for key, value := range extraFields {
		if _, exists := raw[key]; exists {
			continue
		}
		raw[key] = cloneRawJSON(value)
	}
	return json.Marshal(raw)
}

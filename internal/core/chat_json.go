package core

import "github.com/goccy/go-json"

// chatRequestFields is derived from the struct's json tags at package init so
// the known-field list cannot drift from the type definition.
var chatRequestFields = jsonFieldNames(ChatRequest{})

// UnmarshalJSON decodes the typed fields via an alias (so new fields are
// picked up automatically) and captures every other member in ExtraFields.
func (r *ChatRequest) UnmarshalJSON(data []byte) error {
	type alias ChatRequest
	var raw alias
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	extraFields, err := extractUnknownJSONFields(data, chatRequestFields...)
	if err != nil {
		return err
	}

	*r = ChatRequest(raw)
	r.ExtraFields = extraFields
	return nil
}

func (r ChatRequest) MarshalJSON() ([]byte, error) {
	// alias inherits every field (and json tag) from ChatRequest but drops the
	// MarshalJSON method, so json.Marshal uses default struct encoding without
	// recursing. ExtraFields is json:"-", so it is skipped here and merged in
	// separately. Adding a typed field to ChatRequest therefore round-trips
	// automatically instead of being silently dropped.
	type alias ChatRequest
	return marshalWithUnknownJSONFields(alias(r), r.ExtraFields)
}

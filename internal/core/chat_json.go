package core

import "github.com/goccy/go-json"

// chatRequestFields is derived from the struct's json tags at package init so
// the known-field list cannot drift from the type definition.
var chatRequestFields = jsonFieldSetOf(ChatRequest{})

// UnmarshalJSON decodes the typed fields and captures every other member in
// ExtraFields. The messages are decoded in place; a body that path rejects is
// decoded again by the reference decode, so errors are the decoder's own.
func (r *ChatRequest) UnmarshalJSON(data []byte) error {
	if r.unmarshalJSONInPlace(data) != nil {
		return r.unmarshalJSONReference(data)
	}
	return nil
}

//nolint:dupl // same shape as ChatResponse's in-place decode over a different type and member.
func (r *ChatRequest) unmarshalJSONInPlace(data []byte) error {
	type alias ChatRequest
	var raw struct {
		alias
		// Shadows alias.Messages so the decoder only checks the member's
		// shape; the messages are decoded below.
		Messages []struct{} `json:"messages"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	messages, err := decodeJSONObjectSliceMember[Message](data, "messages")
	if err != nil {
		return err
	}
	extraFields, err := extractUnknownJSONFieldsSet(data, chatRequestFields)
	if err != nil {
		return err
	}
	*r = ChatRequest(raw.alias)
	r.Messages = messages
	r.ExtraFields = extraFields
	return nil
}

// unmarshalJSONReference decodes the typed fields via an alias (so new fields
// are picked up automatically) and captures every other member in ExtraFields.
func (r *ChatRequest) unmarshalJSONReference(data []byte) error {
	type alias ChatRequest
	var raw alias
	if err := json.Unmarshal(data, &raw); err != nil {
		return wrapJSONDecodeError(err)
	}

	extraFields, err := extractUnknownJSONFieldsSet(data, chatRequestFields)
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

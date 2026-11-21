package core

import (
	"bytes"

	"github.com/goccy/go-json"
)

// ChatResponse and Choice mirror ChatRequest: known members decode into typed
// fields and every other member is kept in ExtraFields so a provider extension
// the gateway does not model (OpenRouter's native finish reason, for example)
// reaches the client instead of being dropped on re-encoding. Usage keeps its
// own extras in RawUsage and is unchanged.
var (
	chatResponseFields = jsonFieldSetOf(ChatResponse{})
	choiceFields       = jsonFieldSetOf(Choice{})
)

// UnmarshalJSON decodes the choices in place. A body it rejects is decoded
// again by the reference decode, so errors are the decoder's own.
func (r *ChatResponse) UnmarshalJSON(data []byte) error {
	if r.unmarshalJSONInPlace(data) != nil {
		return r.unmarshalJSONReference(data)
	}
	return nil
}

//nolint:dupl // same shape as ChatRequest's in-place decode over a different type and member.
func (r *ChatResponse) unmarshalJSONInPlace(data []byte) error {
	type alias ChatResponse
	var raw struct {
		alias
		// Shadows alias.Choices so the decoder only checks the member's
		// shape; the choices are decoded below.
		Choices []struct{} `json:"choices"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	choices, err := decodeJSONObjectSliceMember[Choice](data, "choices")
	if err != nil {
		return err
	}
	extraFields, err := extractUnknownResponseFields(data, chatResponseFields)
	if err != nil {
		return err
	}
	*r = ChatResponse(raw.alias)
	r.Choices = choices
	r.ExtraFields = extraFields
	return nil
}

func (r *ChatResponse) unmarshalJSONReference(data []byte) error {
	type alias ChatResponse
	var raw alias
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	extraFields, err := extractUnknownResponseFields(data, chatResponseFields)
	if err != nil {
		return err
	}
	*r = ChatResponse(raw)
	r.ExtraFields = extraFields
	return nil
}

func (r ChatResponse) MarshalJSON() ([]byte, error) {
	type alias ChatResponse
	return marshalWithUnknownJSONFields(alias(r), r.ExtraFields)
}

// UnmarshalJSON decodes the message in place, like ChatResponse.UnmarshalJSON.
func (c *Choice) UnmarshalJSON(data []byte) error {
	if c.unmarshalJSONInPlace(data) != nil {
		return c.unmarshalJSONReference(data)
	}
	return nil
}

func (c *Choice) unmarshalJSONInPlace(data []byte) error {
	type alias Choice
	var raw struct {
		alias
		// Shadows alias.Message, decoded below.
		Message struct{} `json:"message"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	// The decoder decodes every occurrence into the same field, so does this.
	var message ResponseMessage
	if err := forEachMatchingJSONMember(data, "message", message.UnmarshalJSON); err != nil {
		return err
	}
	extraFields, err := extractUnknownResponseFields(data, choiceFields)
	if err != nil {
		return err
	}
	*c = Choice(raw.alias)
	c.Message = message
	c.ExtraFields = extraFields
	return nil
}

func (c *Choice) unmarshalJSONReference(data []byte) error {
	type alias Choice
	var raw alias
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	extraFields, err := extractUnknownResponseFields(data, choiceFields)
	if err != nil {
		return err
	}
	*c = Choice(raw)
	c.ExtraFields = extraFields
	return nil
}

func (c Choice) MarshalJSON() ([]byte, error) {
	type alias Choice
	return marshalWithUnknownJSONFields(alias(c), c.ExtraFields)
}

// extractUnknownResponseFields tolerates a JSON null, which providers emit for
// an absent usage block and which the typed decode already treats as empty.
func extractUnknownResponseFields(data []byte, known jsonFieldSet) (UnknownJSONFields, error) {
	if IsJSONNull(bytes.TrimSpace(data)) {
		return UnknownJSONFields{}, nil
	}
	return extractUnknownJSONFieldsSet(data, known)
}

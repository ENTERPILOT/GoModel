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

func (r *ChatResponse) UnmarshalJSON(data []byte) error {
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

func (c *Choice) UnmarshalJSON(data []byte) error {
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

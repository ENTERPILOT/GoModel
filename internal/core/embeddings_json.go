package core

import "github.com/goccy/go-json"

func (r *EmbeddingRequest) UnmarshalJSON(data []byte) error {
	var raw struct {
		Model          string `json:"model"`
		Provider       string `json:"provider,omitempty"`
		Input          any    `json:"input"`
		EncodingFormat string `json:"encoding_format,omitempty"`
		Dimensions     *int   `json:"dimensions,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	extraFields, err := extractUnknownJSONFields(data,
		"model",
		"provider",
		"input",
		"encoding_format",
		"dimensions",
	)
	if err != nil {
		return err
	}

	r.Model = raw.Model
	r.Provider = raw.Provider
	r.Input = raw.Input
	r.EncodingFormat = raw.EncodingFormat
	r.Dimensions = raw.Dimensions
	r.ExtraFields = extraFields
	return nil
}

func (r EmbeddingRequest) MarshalJSON() ([]byte, error) {
	// alias inherits EmbeddingRequest's fields and tags but drops MarshalJSON so
	// json.Marshal does not recurse; ExtraFields (json:"-") is merged separately.
	type alias EmbeddingRequest
	return marshalWithUnknownJSONFields(alias(r), r.ExtraFields)
}

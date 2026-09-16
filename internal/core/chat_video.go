package core

import "github.com/goccy/go-json"

// VideoURLContent contains a video reference and optional processing settings.
// Provider-specific settings are preserved in ExtraFields.
type VideoURLContent struct {
	URL         string            `json:"url"`
	Detail      string            `json:"detail,omitempty"`
	ExtraFields UnknownJSONFields `json:"-" swaggerignore:"true"`
}

func (v *VideoURLContent) UnmarshalJSON(data []byte) error {
	type plain VideoURLContent
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	fields, err := extractUnknownJSONFields(data, "url", "detail")
	if err != nil {
		return err
	}
	*v = VideoURLContent(decoded)
	v.ExtraFields = fields
	return nil
}

func (v VideoURLContent) MarshalJSON() ([]byte, error) {
	return marshalWithUnknownJSONFields(struct {
		URL    string `json:"url"`
		Detail string `json:"detail,omitempty"`
	}{URL: v.URL, Detail: v.Detail}, v.ExtraFields)
}

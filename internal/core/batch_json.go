package core

import "encoding/json"

func (r *BatchRequest) UnmarshalJSON(data []byte) error {
	var raw struct {
		InputFileID      string             `json:"input_file_id,omitempty"`
		Endpoint         string             `json:"endpoint,omitempty"`
		CompletionWindow string             `json:"completion_window,omitempty"`
		Metadata         map[string]string  `json:"metadata,omitempty"`
		Requests         []BatchRequestItem `json:"requests,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	extraFields, err := extractUnknownJSONFields(data,
		"input_file_id",
		"endpoint",
		"completion_window",
		"metadata",
		"requests",
	)
	if err != nil {
		return err
	}

	r.InputFileID = raw.InputFileID
	r.Endpoint = raw.Endpoint
	r.CompletionWindow = raw.CompletionWindow
	r.Metadata = raw.Metadata
	r.Requests = raw.Requests
	r.ExtraFields = extraFields
	return nil
}

func (r BatchRequest) MarshalJSON() ([]byte, error) {
	type batchRequestAlias struct {
		InputFileID      string             `json:"input_file_id,omitempty"`
		Endpoint         string             `json:"endpoint,omitempty"`
		CompletionWindow string             `json:"completion_window,omitempty"`
		Metadata         map[string]string  `json:"metadata,omitempty"`
		Requests         []BatchRequestItem `json:"requests,omitempty"`
	}

	return marshalWithUnknownJSONFields(batchRequestAlias{
		InputFileID:      r.InputFileID,
		Endpoint:         r.Endpoint,
		CompletionWindow: r.CompletionWindow,
		Metadata:         r.Metadata,
		Requests:         r.Requests,
	}, r.ExtraFields)
}

func (r *BatchRequestItem) UnmarshalJSON(data []byte) error {
	var raw struct {
		CustomID string          `json:"custom_id,omitempty"`
		Method   string          `json:"method,omitempty"`
		URL      string          `json:"url"`
		Body     json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	extraFields, err := extractUnknownJSONFields(data,
		"custom_id",
		"method",
		"url",
		"body",
	)
	if err != nil {
		return err
	}

	r.CustomID = raw.CustomID
	r.Method = raw.Method
	r.URL = raw.URL
	r.Body = CloneRawJSON(raw.Body)
	r.ExtraFields = extraFields
	return nil
}

func (r BatchRequestItem) MarshalJSON() ([]byte, error) {
	type batchRequestItemAlias struct {
		CustomID string          `json:"custom_id,omitempty"`
		Method   string          `json:"method,omitempty"`
		URL      string          `json:"url"`
		Body     json.RawMessage `json:"body"`
	}

	return marshalWithUnknownJSONFields(batchRequestItemAlias{
		CustomID: r.CustomID,
		Method:   r.Method,
		URL:      r.URL,
		Body:     CloneRawJSON(r.Body),
	}, r.ExtraFields)
}

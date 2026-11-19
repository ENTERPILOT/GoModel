package core

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeModerationRequest_ForwardsInputAndExtraFieldsVerbatim(t *testing.T) {
	body := []byte(`{"model":"omni-moderation-latest","provider":"openai","input":["a","b"],"future_option":true}`)

	req, err := DecodeModerationRequest(body)
	require.NoError(t, err)
	assert.Equal(t, "omni-moderation-latest", req.Model)
	assert.Equal(t, "openai", req.Provider)

	req.Provider = ""
	out, err := json.Marshal(req)
	require.NoError(t, err)
	assert.JSONEq(t, `{"model":"omni-moderation-latest","input":["a","b"],"future_option":true}`, string(out))
}

func TestDecodeModerationRequest_RejectsMalformedJSON(t *testing.T) {
	_, err := DecodeModerationRequest([]byte(`{"input":`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid moderation request")
}

func TestModerationInputs(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []ModerationInput
		wantErr string
	}{
		{name: "string", input: `"hello"`, want: []ModerationInput{{Text: "hello"}}},
		{name: "array of strings", input: `["a","b"]`, want: []ModerationInput{{Text: "a"}, {Text: "b"}}},
		{
			name:  "multimodal parts are one input",
			input: `[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"https://example.com/a.png"}},{"type":"text","text":"here"}]`,
			want:  []ModerationInput{{Text: "look\nhere", ImageURLs: []string{"https://example.com/a.png"}}},
		},
		{name: "missing", input: ``, wantErr: "input is required"},
		{name: "null", input: `null`, wantErr: "input is required"},
		{name: "empty array", input: `[]`, wantErr: "input must not be empty"},
		{name: "number", input: `42`, wantErr: "input must be a string or an array"},
		{name: "mixed array", input: `["a",{"type":"text","text":"b"}]`, wantErr: "only strings or only text and image_url objects"},
		{name: "unknown part type", input: `[{"type":"audio"}]`, wantErr: `got "audio"`},
		{name: "image part without url", input: `[{"type":"image_url","image_url":{}}]`, wantErr: "image_url.url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &ModerationRequest{Input: json.RawMessage(tt.input)}
			got, err := req.ModerationInputs()
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateModerationRequest_DefaultsModel(t *testing.T) {
	req := &ModerationRequest{Input: json.RawMessage(`"hi"`)}
	require.NoError(t, ValidateModerationRequest(req))
	assert.Equal(t, DefaultModerationModel, req.Model)

	req = &ModerationRequest{Model: "text-moderation-stable", Input: json.RawMessage(`"hi"`)}
	require.NoError(t, ValidateModerationRequest(req))
	assert.Equal(t, "text-moderation-stable", req.Model)

	require.Error(t, ValidateModerationRequest(nil))
	require.Error(t, ValidateModerationRequest(&ModerationRequest{}))
}

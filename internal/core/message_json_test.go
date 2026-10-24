package core

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Marshalling canonicalizes part types and drops the fields foreign to each
// type, and it must keep doing both now that it no longer copies each part's
// unknown members. It must also leave the caller's content untouched.
func TestMessageMarshalCanonicalizesPartsWithoutMutatingThem(t *testing.T) {
	parts := []ContentPart{
		{
			Type:        "input_text",
			Text:        "hello",
			ImageURL:    &ImageURLContent{URL: "https://example.com/stray.png"},
			ExtraFields: UnknownJSONFieldsFromMap(map[string]json.RawMessage{"x_part": json.RawMessage(`{"i":1}`)}),
		},
		{
			Type: "input_image",
			ImageURL: &ImageURLContent{
				URL:         "https://example.com/a.png",
				Detail:      "high",
				ExtraFields: UnknownJSONFieldsFromMap(map[string]json.RawMessage{"x_nested": json.RawMessage(`"image-extra"`)}),
			},
		},
	}
	msg := Message{Role: "user", Content: parts}

	body, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded struct {
		Content []map[string]json.RawMessage `json:"content"`
	}
	require.NoError(t, json.Unmarshal(body, &decoded))
	require.Len(t, decoded.Content, 2)

	assert.JSONEq(t, `"text"`, string(decoded.Content[0]["type"]), "input_text is emitted as text")
	assert.NotContains(t, decoded.Content[0], "image_url", "a text part must not emit a stray image_url")
	assert.JSONEq(t, `{"i":1}`, string(decoded.Content[0]["x_part"]), "unknown members survive")
	assert.JSONEq(t, `"image_url"`, string(decoded.Content[1]["type"]), "input_image is emitted as image_url")
	assert.Contains(t, string(decoded.Content[1]["image_url"]), "image-extra", "nested unknown members survive")

	// The caller still owns exactly what it passed in.
	assert.Equal(t, "input_text", parts[0].Type)
	assert.NotNil(t, parts[0].ImageURL)
	assert.Equal(t, "input_image", parts[1].Type)
}

// An invalid part still fails the marshal rather than emitting a part no
// provider would accept.
func TestMessageMarshalRejectsInvalidPart(t *testing.T) {
	msg := Message{Role: "user", Content: []ContentPart{{Type: "text"}}}
	_, err := json.Marshal(msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "text part is missing text")
}

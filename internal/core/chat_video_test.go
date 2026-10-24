package core

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVideoContentRoundTripAndClone(t *testing.T) {
	raw := `[{"type":"video_url","video_url":{"url":"mm_file://clip","detail":"high","fps":2,"max_long_side_pixel":1280},"custom":"kept"}]`
	content, err := UnmarshalMessageContent([]byte(raw))
	require.NoError(t, err)
	normalized, err := NormalizeMessageContent(content)
	require.NoError(t, err)
	encoded, err := json.Marshal(normalized)
	require.NoError(t, err)
	require.JSONEq(t, raw, string(encoded))
	original := content.([]ContentPart)[0]
	cloned := normalized.([]ContentPart)[0]
	require.NotSame(t, original.VideoURL, cloned.VideoURL)
	cloned.VideoURL.URL = "changed"
	cloned.VideoURL.ExtraFields.Lookup("fps")[0] = '3'
	cloned.ExtraFields.Lookup("custom")[1] = 'x'
	encoded, err = json.Marshal(content)
	require.NoError(t, err)
	require.JSONEq(t, raw, string(encoded))

	var dynamic []any
	require.NoError(t, json.Unmarshal([]byte(raw), &dynamic))
	normalized, err = NormalizeMessageContent(dynamic)
	require.NoError(t, err)
	encoded, err = json.Marshal(normalized)
	require.NoError(t, err)
	require.JSONEq(t, raw, string(encoded))
}

func TestVideoContentRejectsMissingURL(t *testing.T) {
	for _, raw := range []string{
		`[{"type":"video_url"}]`,
		`[{"type":"video_url","video_url":null}]`,
		`[{"type":"video_url","video_url":{}}]`,
		`[{"type":"video_url","video_url":{"url":" "}}]`,
		`[{"type":"video_url","video_url":"clip"}]`,
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := UnmarshalMessageContent([]byte(raw))
			require.Error(t, err)
		})
	}
	for _, video := range []*VideoURLContent{nil, {}, {URL: " "}} {
		part := ContentPart{Type: "video_url", VideoURL: video}
		_, err := json.Marshal(part)
		require.Error(t, err)
		_, err = NormalizeMessageContent([]ContentPart{part})
		require.Error(t, err)
	}
}

func TestVideoURLContentMarshalRejectsBlankURL(t *testing.T) {
	for _, video := range []VideoURLContent{{}, {URL: " \t "}, {URL: "", Detail: "high"}} {
		_, err := json.Marshal(video)
		require.Error(t, err)
	}

	encoded, err := json.Marshal(VideoURLContent{URL: "mm_file://clip"})
	require.NoError(t, err)
	require.JSONEq(t, `{"url":"mm_file://clip"}`, string(encoded))
}

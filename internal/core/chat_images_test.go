package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChatImageEntriesAndParts(t *testing.T) {
	const valid = `{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1n"}}`
	tests := []struct {
		name string
		raw  string
		want int
	}{
		{name: "valid entries", raw: `[` + valid + `,` + valid + `]`, want: 2},
		{name: "malformed entries skipped", raw: `[` + valid + `,{"type":"text","text":"x"},{"type":"image_url"},{"type":"image_url","image_url":{"url":""}},42]`, want: 1},
		{name: "not a list", raw: `{"type":"image_url"}`, want: 0},
		{name: "empty", raw: ``, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries := ChatImageEntries([]byte(tt.raw))
			require.Len(t, entries, tt.want)
			for _, entry := range entries {
				assert.JSONEq(t, valid, string(entry))
			}

			parts := ChatImageParts([]byte(tt.raw))
			require.Len(t, parts, tt.want)
			for _, part := range parts {
				require.NotNil(t, part.ImageURL)
				assert.Equal(t, "data:image/png;base64,aW1n", part.ImageURL.URL)
			}
		})
	}
}

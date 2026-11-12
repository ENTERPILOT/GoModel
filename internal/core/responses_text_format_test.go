package core

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponsesTextFormat(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    map[string]any
		wantErr bool
	}{
		{name: "json_object", raw: `{"type":"json_object"}`, want: map[string]any{"type": "json_object"}},
		{name: "json_schema flattens", raw: `{"type":"json_schema","json_schema":{"name":"s","strict":true}}`, want: map[string]any{"type": "json_schema", "name": "s", "strict": true}},
		{name: "a schema member named type keeps the discriminator", raw: `{"type":"json_schema","json_schema":{"name":"s","type":"object"}}`, want: map[string]any{"type": "json_schema", "name": "s"}},
		{name: "json_schema without a schema object", raw: `{"type":"json_schema"}`, want: map[string]any{"type": "json_schema"}},
		{name: "unknown type", raw: `{"type":"regex"}`, wantErr: true},
		{name: "not an object", raw: `"json_object"`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResponsesTextFormat(json.RawMessage(tt.raw))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

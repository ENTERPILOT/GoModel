package providers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLabelList(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    map[string]string
		wantErr string
	}{
		{name: "one label", value: "code-review=private", want: map[string]string{"code-review": "private"}},
		{name: "several labels, spaces trimmed", value: " code-review = private , team=search ", want: map[string]string{"code-review": "private", "team": "search"}},
		{name: "empty value allowed", value: "team=", want: map[string]string{"team": ""}},
		{name: "blank entries skipped", value: "team=search,,", want: map[string]string{"team": "search"}},
		{name: "blank", value: "  "},
		{name: "only separators", value: ",,"},
		{name: "entry without equals", value: "team=search,private", wantErr: "entry 2 is not key=value"},
		{name: "empty key", value: "=private", wantErr: "entry 1 is not key=value"},
		{name: "repeated key", value: "team=a,team=b", wantErr: "entry 2 repeats a key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseLabelList(tt.value)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

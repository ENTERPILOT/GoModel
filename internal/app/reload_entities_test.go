package app

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKeepServingEntities(t *testing.T) {
	serving := func(name string) bool { return name == "openai" }
	tests := []struct {
		name    string
		skipped map[string]error
		wantErr []string
	}{
		{name: "nothing skipped"},
		{name: "skipped but not serving", skipped: map[string]error{"broken": errors.New("provider_credentials.broken.api_keys[0]: gone")}},
		{
			name: "serving entities block the reload",
			skipped: map[string]error{
				"openai": errors.New("provider_credentials.openai.api_keys[2]: gone"),
				"broken": errors.New("provider_credentials.broken.api_keys[0]: gone"),
			},
			wantErr: []string{"serving provider credentials would be dropped", `"openai": provider_credentials.openai.api_keys[2]`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := keepServingEntities("provider credentials", tt.skipped, serving)
			if len(tt.wantErr) == 0 {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, want := range tt.wantErr {
				assert.Contains(t, err.Error(), want)
			}
			assert.NotContains(t, err.Error(), "broken", "an entity the serving generation skipped too is not named")
		})
	}
}

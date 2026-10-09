package app

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKeepServingEntities(t *testing.T) {
	serving := func(name string) bool { return name == "openai" || name == "github" }
	tests := []struct {
		name       string
		unresolved map[string]error
		wantErr    []string
	}{
		{name: "nothing unresolved"},
		{name: "unresolved but not serving", unresolved: map[string]error{"broken": errors.New("provider_credentials.broken.api_keys[0]: gone")}},
		{
			name: "serving entities block the reload",
			unresolved: map[string]error{
				"openai": errors.New("provider_credentials.openai.api_keys[2]: gone"),
				"broken": errors.New("provider_credentials.broken.api_keys[0]: gone"),
			},
			wantErr: []string{"provider credentials that are serving", "provider_credentials.openai.api_keys[2]"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := keepServingEntities("provider credentials", tt.unresolved, serving)
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

package core

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseModelSelector(t *testing.T) {
	tests := []struct {
		name          string
		model         string
		provider      string
		wantModel     string
		wantProvider  string
		wantQualified string
		wantErr       bool
	}{
		{
			name:          "plain model",
			model:         "gpt-4o",
			wantModel:     "gpt-4o",
			wantProvider:  "",
			wantQualified: "gpt-4o",
		},
		{
			name:          "prefixed model",
			model:         "openai/gpt-4o",
			wantModel:     "gpt-4o",
			wantProvider:  "openai",
			wantQualified: "openai/gpt-4o",
		},
		{
			name:          "provider field",
			model:         "gpt-4o",
			provider:      "openai",
			wantModel:     "gpt-4o",
			wantProvider:  "openai",
			wantQualified: "openai/gpt-4o",
		},
		{
			name:          "matching provider prefix normalizes once",
			model:         "openai/gpt-4o",
			provider:      "openai",
			wantModel:     "gpt-4o",
			wantProvider:  "openai",
			wantQualified: "openai/gpt-4o",
		},
		{
			name:          "explicit provider keeps slash model raw",
			model:         "openai/gpt-oss-120b",
			provider:      "groq",
			wantModel:     "openai/gpt-oss-120b",
			wantProvider:  "groq",
			wantQualified: "groq/openai/gpt-oss-120b",
		},
		{
			name:    "missing model",
			model:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selector, err := ParseModelSelector(tt.model, tt.provider)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantModel, selector.Model)
			require.Equal(t, tt.wantProvider, selector.Provider)
			require.Equal(t, tt.wantQualified, selector.QualifiedModel())
		})
	}
}

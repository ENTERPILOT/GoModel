package config

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckSecretDestinations(t *testing.T) {
	repointed := []SecretDestination{
		{Field: "type", Stored: "openai", Saved: "openai"},
		{Field: "base_url", Stored: "https://api.example.com", Saved: "https://attacker.example"},
	}
	unchanged := []SecretDestination{{Field: "base_url", Stored: "https://api.example.com", Saved: " https://api.example.com "}}
	tests := []struct {
		name         string
		restricted   bool
		destinations []SecretDestination
		values       []string
		wantErr      bool
	}{
		{name: "repointed with a reference", restricted: true, destinations: repointed, values: []string{"sk-literal", "${env:KEY}"}, wantErr: true},
		{name: "repointed with a reference in other text", restricted: true, destinations: repointed, values: []string{"Bearer ${file:/run/secrets/key}"}, wantErr: true},
		{name: "repointed with literals only", restricted: true, destinations: repointed, values: []string{"sk-literal", "$${env:KEY}", "${LEGACY}"}},
		{name: "repointed with no secrets", restricted: true, destinations: repointed},
		{name: "unchanged destination", restricted: true, destinations: unchanged, values: []string{"${env:KEY}"}},
		{name: "new entity", restricted: true, values: []string{"${env:KEY}"}},
		{name: "unrestricted", destinations: repointed, values: []string{"${env:KEY}"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			if tt.restricted {
				ctx = RestrictSecretReferences(ctx)
			}
			err := CheckSecretDestinations(ctx, "provider_credentials", "acme", tt.destinations, tt.values)
			if !tt.wantErr {
				assert.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrSecretDestinationRestricted)
			secretErr, ok := errors.AsType[*SecretError](err)
			require.True(t, ok)
			assert.Equal(t, "provider_credentials.acme.base_url", secretErr.Field)
			assert.NotContains(t, err.Error(), "attacker")
			assert.NotContains(t, err.Error(), "KEY")
		})
	}
}

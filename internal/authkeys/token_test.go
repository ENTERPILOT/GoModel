package authkeys

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSecretLengthMatchesEncoding(t *testing.T) {
	assert.Equal(t, secretLength, base64.RawURLEncoding.EncodedLen(secretBytes))
}

func TestNormalizeTokenPrefix(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr string
	}{
		{name: "empty is default", in: "", want: DefaultTokenPrefix},
		{name: "blank is default", in: "  \t", want: DefaultTokenPrefix},
		{name: "default", in: "sk_gom_", want: "sk_gom_"},
		{name: "dash prefix", in: "sk-customnamehere-", want: "sk-customnamehere-"},
		{name: "trimmed", in: " acme_ ", want: "acme_"},
		{name: "digits and case", in: "Org42-", want: "Org42-"},
		{name: "single character", in: "k", want: "k"},
		{name: "maximum length", in: strings.Repeat("a", MaxTokenPrefixLength), want: strings.Repeat("a", MaxTokenPrefixLength)},
		{name: "too long", in: strings.Repeat("a", MaxTokenPrefixLength+1), wantErr: "at most 32 characters"},
		{name: "inner space", in: "sk acme", wantErr: "only ASCII letters"},
		{name: "dot", in: "sk.acme.", wantErr: "only ASCII letters"},
		{name: "colon", in: "sk:acme", wantErr: "only ASCII letters"},
		{name: "dollar", in: "sk$", wantErr: "only ASCII letters"},
		{name: "quote", in: `sk"`, wantErr: "only ASCII letters"},
		{name: "slash", in: "sk/acme", wantErr: "only ASCII letters"},
		{name: "non-ASCII", in: "sk_ä_", wantErr: "only ASCII letters"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeTokenPrefix(tt.in)
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

// A key pasted where the prefix belongs must not reach the startup log.
func TestNormalizeTokenPrefixErrorOmitsValue(t *testing.T) {
	for _, value := range []string{"sk bad!", strings.Repeat("a", MaxTokenPrefixLength+1)} {
		_, err := NormalizeTokenPrefix(value)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), value)
	}
}

func TestNewServiceRejectsInvalidTokenPrefix(t *testing.T) {
	_, err := NewService(newTestStore(), WithTokenPrefix("sk acme"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth key prefix")
}

func TestServiceCreateUsesTokenPrefix(t *testing.T) {
	tests := []struct {
		name   string
		opts   []Option
		prefix string
	}{
		{name: "no option", prefix: DefaultTokenPrefix},
		{name: "empty option", opts: []Option{WithTokenPrefix("")}, prefix: DefaultTokenPrefix},
		{name: "custom", opts: []Option{WithTokenPrefix("acme_")}, prefix: "acme_"},
		{name: "sk dash", opts: []Option{WithTokenPrefix("sk-customnamehere-")}, prefix: "sk-customnamehere-"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, err := NewService(newTestStore(), tt.opts...)
			require.NoError(t, err)

			issued, err := service.Create(context.Background(), CreateInput{Name: "k"})
			require.NoError(t, err)
			secret, ok := strings.CutPrefix(issued.Value, tt.prefix)
			require.True(t, ok, "value %q", issued.Value)
			assert.Len(t, secret, secretLength)
			assert.Equal(t, tt.prefix+"..."+secret[len(secret)-4:], issued.RedactedValue)
			assert.Equal(t, hashSecret(secret), issued.SecretHash)

			got, err := service.Authenticate(context.Background(), issued.Value)
			require.NoError(t, err)
			assert.Equal(t, issued.ID, got.ID)
		})
	}
}

// TestServiceAuthenticatesKeysIssuedUnderEarlierPrefixes issues one key per
// prefix in turn against one store, as an operator changing the setting
// between restarts would, and checks every gateway generation still accepts
// every key issued before it.
func TestServiceAuthenticatesKeysIssuedUnderEarlierPrefixes(t *testing.T) {
	ctx := context.Background()
	store := newTestStore()
	prefixes := []string{"", "acme_", "sk-team-", "sk_gom_acme-", "k", strings.Repeat("Z", MaxTokenPrefixLength), DefaultTokenPrefix}

	var issued []*IssuedKey
	for _, prefix := range prefixes {
		service, err := NewService(store, WithTokenPrefix(prefix))
		require.NoError(t, err)
		require.NoError(t, service.Refresh(ctx))

		key, err := service.Create(ctx, CreateInput{Name: "key-" + prefix})
		require.NoError(t, err)
		issued = append(issued, key)

		for _, earlier := range issued {
			got, err := service.Authenticate(ctx, earlier.Value)
			require.NoError(t, err, "prefix %q, token %q", prefix, earlier.Value)
			assert.Equal(t, earlier.ID, got.ID)

			// The right prefix with a wrong secret is still rejected.
			wrong := []byte(earlier.Value)
			wrong[len(wrong)-1] ^= 1
			_, err = service.Authenticate(ctx, string(wrong))
			require.ErrorIs(t, err, ErrInvalidToken, "token %q", wrong)
			_, err = service.Authenticate(ctx, earlier.Value[:len(earlier.Value)-secretLength])
			require.ErrorIs(t, err, ErrInvalidToken, "bare prefix of %q", earlier.Value)
		}
	}
}

func TestServiceAuthenticateLegacyDefaultPrefixSecrets(t *testing.T) {
	// Keys written straight to storage under the default prefix, such as the
	// demo seed's hex secrets, keep the exact check they always had.
	now := time.Now().UTC()
	hexSecret := strings.Repeat("ab", 24)
	key := AuthKey{ID: "seeded", Name: "seeded", SecretHash: hashSecret(hexSecret), Enabled: true, CreatedAt: now, UpdatedAt: now}
	service, err := NewService(newTestStore(key), WithTokenPrefix("acme_"))
	require.NoError(t, err)
	require.NoError(t, service.Refresh(context.Background()))

	got, err := service.Authenticate(context.Background(), DefaultTokenPrefix+hexSecret)
	require.NoError(t, err)
	assert.Equal(t, "seeded", got.ID)

	for _, token := range []string{"acme_" + hexSecret, DefaultTokenPrefix, DefaultTokenPrefix + hexSecret + "x"} {
		_, err := service.Authenticate(context.Background(), token)
		require.ErrorIs(t, err, ErrInvalidToken, "token %q", token)
	}
}

func TestServiceAuthenticateSkPrefixAlongsideLiteLLMImports(t *testing.T) {
	ctx := context.Background()
	service, err := NewService(newTestStore(), WithTokenPrefix("sk-customnamehere-"))
	require.NoError(t, err)

	issued, err := service.Create(ctx, CreateInput{Name: "native"})
	require.NoError(t, err)

	// A short LiteLLM token, and one long enough that its tail looks like a
	// GoModel secret under the "sk-" prefix.
	longLiteLLMToken := "sk-" + strings.Repeat("Q", secretLength)
	short := importLiteLLMKey(t, service, ImportInput{Name: "short"})
	long := importLiteLLMKey(t, service, ImportInput{Name: "long", SecretHash: hashSecret(longLiteLLMToken)})
	// An imported key hashed like a GoModel secret must not be reached
	// through the GoModel path.
	decoyTail := strings.Repeat("D", secretLength)
	importLiteLLMKey(t, service, ImportInput{Name: "decoy", SecretHash: hashSecret(decoyTail)})
	require.NoError(t, service.Refresh(ctx))

	tests := []struct {
		name   string
		token  string
		wantID string
	}{
		{name: "issued key", token: issued.Value, wantID: issued.ID},
		{name: "short LiteLLM key", token: liteLLMToken, wantID: short.ID},
		{name: "long LiteLLM key", token: longLiteLLMToken, wantID: long.ID},
		{name: "GoModel shape over an imported hash", token: "sk-customnamehere-" + decoyTail},
		{name: "issued secret alone", token: strings.TrimPrefix(issued.Value, "sk-customnamehere-")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := service.Authenticate(ctx, tt.token)
			if tt.wantID == "" {
				require.ErrorIs(t, err, ErrInvalidToken)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, got.ID)
		})
	}
}

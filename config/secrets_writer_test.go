package config

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSecretWriter stores secrets in memory under ${fake:<n>} references.
type fakeSecretWriter struct {
	written   map[string]string
	keys      []SecretKey
	deleted   []string
	reference string // returned instead of a generated one when set
	writeErr  error
	deleteErr error
}

func (w *fakeSecretWriter) WriteSecret(_ context.Context, key SecretKey, value string) (string, error) {
	if w.writeErr != nil {
		return "", w.writeErr
	}
	if w.written == nil {
		w.written = map[string]string{}
	}
	reference := w.reference
	if reference == "" {
		reference = "${fake:" + key.Entity + "/" + key.ID + "/" + key.Field + "}"
	}
	w.written[reference] = value
	w.keys = append(w.keys, key)
	return reference, nil
}

func (w *fakeSecretWriter) DeleteSecret(_ context.Context, reference string) error {
	w.deleted = append(w.deleted, reference)
	return w.deleteErr
}

func (w *fakeSecretWriter) OwnsReference(reference string) bool {
	return strings.HasPrefix(reference, "${fake:")
}

func TestStoreSecretWithoutWriterKeepsValue(t *testing.T) {
	stored, err := NewSecrets().StoreSecret(t.Context(), SecretKey{Entity: "e", ID: "id", Field: "f"}, "sk-literal", nil)
	require.NoError(t, err)
	assert.Equal(t, "sk-literal", stored)
}

func TestStoreSecretWritesLiteralsOnly(t *testing.T) {
	writer := &fakeSecretWriter{}
	secrets := NewSecrets()
	secrets.SetWriter(writer)
	key := SecretKey{Entity: "provider_credentials", ID: "openai", Field: "api_keys[0]"}

	stored, err := secrets.StoreSecret(t.Context(), key, "sk-literal", nil)
	require.NoError(t, err)
	assert.Equal(t, "${fake:provider_credentials/openai/api_keys[0]}", stored)
	assert.Equal(t, []SecretKey{key}, writer.keys)

	for _, value := range []string{"", "${env:KEY}", "Bearer ${env:KEY}"} {
		stored, err = secrets.StoreSecret(t.Context(), key, value, nil)
		require.NoError(t, err)
		assert.Equal(t, value, stored)
	}
	assert.Len(t, writer.keys, 1)
}

func TestStoreSecretErrors(t *testing.T) {
	secrets := NewSecrets()
	secrets.SetWriter(&fakeSecretWriter{writeErr: errors.New("backend down")})
	_, err := secrets.StoreSecret(t.Context(), SecretKey{Entity: "e", ID: "id", Field: "f"}, "s3cret", nil)
	require.ErrorContains(t, err, "backend down")
	assert.NotContains(t, err.Error(), "s3cret")

	for _, reference := range []string{
		"not-a-reference",
		"s3cret ${fake:a}",
		"${fake:a} s3cret",
		"${fake:a}${fake:b}",
		"$${fake:a}",
		"${fake:a",
		"${:a}",
	} {
		secrets.SetWriter(&fakeSecretWriter{reference: reference})
		_, err = secrets.StoreSecret(t.Context(), SecretKey{Entity: "e", ID: "id", Field: "f"}, "s3cret", nil)
		require.ErrorContains(t, err, "did not return a single ${scheme:reference}", reference)
		assert.NotContains(t, err.Error(), "s3cret")
	}
}

// The writer stores what the field would resolve to without one, so an
// escaped ${ reaches it unescaped.
func TestStoreSecretWritesTheResolvedLiteral(t *testing.T) {
	tests := map[string]string{
		"sk-$${abc}":             "sk-${abc}",
		"$$${x}":                 "$${x}",
		"${x$${":                 "${x$${",
		"plain":                  "plain",
		"pre $${a} $${b:c} post": "pre ${a} ${b:c} post",
	}
	for value, want := range tests {
		writer := &fakeSecretWriter{}
		secrets := NewSecrets()
		secrets.SetWriter(writer)
		stored, err := secrets.StoreSecret(t.Context(), SecretKey{Entity: "e", ID: "id", Field: "f"}, value, nil)
		require.NoError(t, err, value)
		assert.Equal(t, want, writer.written[stored], value)

		withoutWriter, err := NewSecrets().Resolve(t.Context(), value)
		require.NoError(t, err)
		assert.Equal(t, withoutWriter, writer.written[stored], "a writer must not change what %q resolves to", value)
	}
}

func TestStoreSecretRestrictedAcceptsOnlyHeldReferences(t *testing.T) {
	key := SecretKey{Entity: "mcp_servers", ID: "docs", Field: "headers.Authorization"}
	held := []string{"Bearer ${env:HELD}", "sk-${file:/run/secrets/tail}"}
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "held reference", value: "${env:HELD}"},
		{name: "held reference in other text", value: "Token ${env:HELD}"},
		{name: "two held references", value: "${env:HELD}${file:/run/secrets/tail}"},
		{name: "literal", value: "sk-literal"},
		{name: "escaped reference", value: "$${env:GOMODEL_MASTER_KEY}"},
		{name: "legacy placeholder", value: "${GOMODEL_MASTER_KEY}"},
		{name: "new reference", value: "${env:GOMODEL_MASTER_KEY}", wantErr: true},
		{name: "new reference in other text", value: "Bearer ${file:/etc/passwd}", wantErr: true},
		{name: "new reference beside a held one", value: "${env:HELD}${env:OTHER}", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := RestrictSecretReferences(t.Context())
			stored, err := NewSecrets().StoreSecret(ctx, key, tt.value, held)
			if !tt.wantErr {
				require.NoError(t, err)
				assert.Equal(t, tt.value, stored)
				return
			}
			require.ErrorIs(t, err, ErrSecretReferenceRestricted)
			secretErr, ok := errors.AsType[*SecretError](err)
			require.True(t, ok)
			assert.Equal(t, "mcp_servers.docs.headers.Authorization", secretErr.Field)
			assert.NotContains(t, err.Error(), "GOMODEL_MASTER_KEY")
			assert.NotContains(t, err.Error(), "/etc/passwd")

			stored, err = NewSecrets().StoreSecret(t.Context(), key, tt.value, held)
			require.NoError(t, err, "an unrestricted context saves any reference")
			assert.Equal(t, tt.value, stored)
		})
	}
}

func TestStoreSecretRestrictedStillWritesLiterals(t *testing.T) {
	writer := &fakeSecretWriter{}
	secrets := NewSecrets()
	secrets.SetWriter(writer)
	key := SecretKey{Entity: "provider_credentials", ID: "openai", Field: "api_keys[0]"}

	stored, err := secrets.StoreSecret(RestrictSecretReferences(t.Context()), key, "sk-literal", nil)
	require.NoError(t, err)
	assert.Equal(t, "${fake:provider_credentials/openai/api_keys[0]}", stored)
	assert.Equal(t, "sk-literal", writer.written[stored])
}

func TestStoreSecretRejectsOwnedReferencesTheEntityDoesNotHold(t *testing.T) {
	writer := &fakeSecretWriter{}
	secrets := NewSecrets()
	// A writer's scheme is registered with it, so errors can name it.
	require.NoError(t, secrets.Register("fake", SecretResolverFunc(func(context.Context, string) (string, error) { return "", nil })))
	key := SecretKey{Entity: "mcp_servers", ID: "docs", Field: "headers.Authorization"}
	owned := "${fake:mcp_servers/docs/headers.Authorization}"

	stored, err := secrets.StoreSecret(t.Context(), key, owned, nil)
	require.NoError(t, err, "without a writer no reference is owned")
	assert.Equal(t, owned, stored)

	secrets.SetWriter(writer)
	stored, err = secrets.StoreSecret(t.Context(), key, owned, []string{"literal", owned})
	require.NoError(t, err, "the stored row still holds it")
	assert.Equal(t, owned, stored)

	stored, err = secrets.StoreSecret(t.Context(), key, "${env:HAND}", nil)
	require.NoError(t, err, "a reference the writer does not own is the operator's")
	assert.Equal(t, "${env:HAND}", stored)

	for _, held := range [][]string{nil, {"${fake:mcp_servers/docs/other}"}} {
		_, err = secrets.StoreSecret(t.Context(), key, owned, held)
		require.ErrorIs(t, err, ErrSecretNotHeld)
		secretErr, ok := errors.AsType[*SecretError](err)
		require.True(t, ok)
		assert.Equal(t, "mcp_servers.docs.headers.Authorization", secretErr.Field)
		assert.Equal(t, "fake", secretErr.Scheme)
		assert.NotContains(t, err.Error(), "mcp_servers/docs")
	}
	assert.Empty(t, writer.keys)
}

// An owned reference inside a longer value is checked, kept, and released
// like one that is the whole value.
func TestWriterReferencesInsideLongerValues(t *testing.T) {
	writer := &fakeSecretWriter{}
	secrets := NewSecrets()
	secrets.SetWriter(writer)
	key := SecretKey{Entity: "mcp_servers", ID: "docs", Field: "headers.Authorization"}

	_, err := secrets.StoreSecret(t.Context(), key, "Bearer ${fake:a}", []string{"${fake:b}"})
	require.ErrorIs(t, err, ErrSecretNotHeld)
	_, err = secrets.StoreSecret(t.Context(), key, "${env:X}-${fake:a}", nil)
	require.ErrorIs(t, err, ErrSecretNotHeld)

	stored, err := secrets.StoreSecret(t.Context(), key, "Bearer ${fake:a}", []string{"${fake:a}"})
	require.NoError(t, err, "the stored row holds it whole")
	assert.Equal(t, "Bearer ${fake:a}", stored)
	stored, err = secrets.StoreSecret(t.Context(), key, "${fake:a}", []string{"Bearer ${fake:a}"})
	require.NoError(t, err, "the stored row holds it inside a longer value")
	assert.Equal(t, "${fake:a}", stored)
	stored, err = secrets.StoreSecret(t.Context(), key, "Bearer $${fake:a}", nil)
	require.NoError(t, err, "an escaped placeholder is literal text, written like any literal")
	assert.Equal(t, "${fake:mcp_servers/docs/headers.Authorization}", stored)
	writer.keys = nil

	require.NoError(t, secrets.ReleaseSecrets(t.Context(), []string{"${fake:a}"}, []string{"Bearer ${fake:a}"}))
	assert.Empty(t, writer.deleted, "a reference still used inside a longer value is kept")

	previous := []string{"Bearer ${fake:a}", "${env:X}:${fake:b}", "$${fake:c}"}
	require.NoError(t, secrets.ReleaseSecrets(t.Context(), previous, []string{"${fake:b}"}))
	assert.Equal(t, []string{"${fake:a}"}, writer.deleted)
}

func TestReleaseSecretsDeletesOwnedReplacedReferences(t *testing.T) {
	writer := &fakeSecretWriter{}
	secrets := NewSecrets()
	require.NoError(t, secrets.ReleaseSecrets(t.Context(), []string{"${fake:a}"}, nil), "no writer, nothing to do")

	secrets.SetWriter(writer)
	previous := []string{"${fake:a}", "${fake:b}", "${env:HAND}", "literal", "${fake:a}"}
	current := []string{"${fake:b}"}
	require.NoError(t, secrets.ReleaseSecrets(t.Context(), previous, current))
	assert.Equal(t, []string{"${fake:a}"}, writer.deleted)

	writer.deleteErr = errors.New("gone")
	require.ErrorContains(t, secrets.ReleaseSecrets(t.Context(), []string{"${fake:c}"}, nil), "gone")
}

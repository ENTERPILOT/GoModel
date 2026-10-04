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

	secrets.SetWriter(&fakeSecretWriter{reference: "not-a-reference"})
	_, err = secrets.StoreSecret(t.Context(), SecretKey{Entity: "e", ID: "id", Field: "f"}, "s3cret", nil)
	require.ErrorContains(t, err, "did not return")
}

func TestStoreSecretRejectsOwnedReferencesTheEntityDoesNotHold(t *testing.T) {
	writer := &fakeSecretWriter{}
	secrets := NewSecrets()
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

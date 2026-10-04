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
	stored, err := NewSecrets().StoreSecret(t.Context(), SecretKey{Entity: "e", ID: "id", Field: "f"}, "sk-literal")
	require.NoError(t, err)
	assert.Equal(t, "sk-literal", stored)
}

func TestStoreSecretWritesLiteralsOnly(t *testing.T) {
	writer := &fakeSecretWriter{}
	secrets := NewSecrets()
	secrets.SetWriter(writer)
	key := SecretKey{Entity: "provider_credentials", ID: "openai", Field: "api_keys[0]"}

	stored, err := secrets.StoreSecret(t.Context(), key, "sk-literal")
	require.NoError(t, err)
	assert.Equal(t, "${fake:provider_credentials/openai/api_keys[0]}", stored)
	assert.Equal(t, []SecretKey{key}, writer.keys)

	for _, value := range []string{"", "${env:KEY}", "Bearer ${env:KEY}"} {
		stored, err = secrets.StoreSecret(t.Context(), key, value)
		require.NoError(t, err)
		assert.Equal(t, value, stored)
	}
	assert.Len(t, writer.keys, 1)
}

func TestStoreSecretErrors(t *testing.T) {
	secrets := NewSecrets()
	secrets.SetWriter(&fakeSecretWriter{writeErr: errors.New("backend down")})
	_, err := secrets.StoreSecret(t.Context(), SecretKey{Entity: "e", ID: "id", Field: "f"}, "s3cret")
	require.ErrorContains(t, err, "backend down")
	assert.NotContains(t, err.Error(), "s3cret")

	secrets.SetWriter(&fakeSecretWriter{reference: "not-a-reference"})
	_, err = secrets.StoreSecret(t.Context(), SecretKey{Entity: "e", ID: "id", Field: "f"}, "s3cret")
	require.ErrorContains(t, err, "did not return")
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

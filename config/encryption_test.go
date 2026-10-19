package config

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_EncryptionKeyFromEnvironment(t *testing.T) {
	clearAllConfigEnvVars(t)
	withTempDir(t, func(string) {
		result, err := Load()
		require.NoError(t, err)
		assert.Empty(t, result.Config.Storage.EncryptionKey, "encryption is off by default")
		assert.Nil(t, result.KeyWrapper())

		t.Setenv("GOMODEL_ENCRYPTION_KEY", "new-key")
		t.Setenv("GOMODEL_ENCRYPTION_KEY_PREVIOUS", "old-key")
		result, err = Load()
		require.NoError(t, err)
		assert.Equal(t, "new-key", result.Config.Storage.EncryptionKey)
		assert.Equal(t, "old-key", result.Config.Storage.EncryptionKeyPrevious)
	})
}

func TestLoad_EncryptionKeyFromYAML(t *testing.T) {
	clearAllConfigEnvVars(t)
	withTempDir(t, func(dir string) {
		writeConfigYAML(t, dir, "storage:\n  encryption_key: yaml-key\n  encryption_key_previous: yaml-old\n")
		result, err := Load()
		require.NoError(t, err)
		assert.Equal(t, "yaml-key", result.Config.Storage.EncryptionKey)
		assert.Equal(t, "yaml-old", result.Config.Storage.EncryptionKeyPrevious)
	})
}

type stubKeyWrapper struct{}

func (stubKeyWrapper) ID() string                                        { return "stub" }
func (stubKeyWrapper) WrapKey(context.Context, []byte) ([]byte, error)   { return nil, nil }
func (stubKeyWrapper) UnwrapKey(context.Context, []byte) ([]byte, error) { return nil, nil }

func TestLoadResultKeyWrapper(t *testing.T) {
	result := &LoadResult{}
	result.SetKeyWrapper(stubKeyWrapper{})
	assert.Equal(t, stubKeyWrapper{}, result.KeyWrapper())
	assert.Empty(t, result.PreviousKeyWrappers())
	result.SetKeyWrapper(stubKeyWrapper{}, stubKeyWrapper{})
	assert.Equal(t, []KeyWrapper{stubKeyWrapper{}}, result.PreviousKeyWrappers())
	result.SetKeyWrapper(nil)
	assert.Nil(t, result.KeyWrapper())
	assert.Empty(t, result.PreviousKeyWrappers())

	var nilResult *LoadResult
	nilResult.SetKeyWrapper(stubKeyWrapper{})
	assert.Nil(t, nilResult.KeyWrapper())
	assert.Nil(t, nilResult.PreviousKeyWrappers())
}

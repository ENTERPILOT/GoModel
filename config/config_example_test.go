package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_FromEnvironment(t *testing.T) {
	_ = os.Setenv("PORT", "9090")
	defer func() {
		_ = os.Unsetenv("PORT")
	}()

	result, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "9090", result.Config.Server.Port)
}

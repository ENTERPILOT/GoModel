package cache

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRedisStoreInvalidURLHidesPassword(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{name: "bad escape", url: "redis://user:s3cret%zz@cache.example.com:6379"},
		{name: "bad host", url: "redis://user:s3cret@cache example.com:6379"},
		{name: "bad database", url: "redis://user:s3cret@cache.example.com:6379/abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewRedisStore(RedisStoreConfig{URL: tt.url})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid redis URL")
			assert.NotContains(t, err.Error(), "s3cret")
		})
	}
}

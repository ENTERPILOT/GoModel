package cache

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRedisStoreInvalidURLHidesPassword(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		password string
	}{
		{name: "bad escape", url: "redis://user:s3cret%zz@cache.example.com:6379", password: "s3cret"},
		{name: "password is the bad escape", url: "redis://user:%zz@cache.example.com:6379", password: "%zz"},
		{name: "bad host", url: "redis://user:s3cret@cache example.com:6379", password: "s3cret"},
		{name: "bad database", url: "redis://user:s3cret@cache.example.com:6379/abc", password: "s3cret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewRedisStore(RedisStoreConfig{URL: tt.url})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid redis URL")
			assert.NotContains(t, err.Error(), tt.password)
		})
	}
}

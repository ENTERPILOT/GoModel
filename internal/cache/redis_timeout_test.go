package cache

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedisOptionsTimeouts(t *testing.T) {
	tests := []struct {
		name  string
		url   string
		dial  time.Duration
		read  time.Duration
		write time.Duration
	}{
		{
			name:  "defaults when unset",
			url:   "redis://localhost:6379",
			dial:  defaultRedisDialTimeout,
			read:  defaultRedisIOTimeout,
			write: defaultRedisIOTimeout,
		},
		{
			name:  "url overrides",
			url:   "redis://localhost:6379?dial_timeout=3s&read_timeout=4s&write_timeout=5s",
			dial:  3 * time.Second,
			read:  4 * time.Second,
			write: 5 * time.Second,
		},
		{
			name:  "url disables read timeout",
			url:   "rediss://localhost:6380?read_timeout=0",
			dial:  defaultRedisDialTimeout,
			read:  -1,
			write: defaultRedisIOTimeout,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := redisOptions(tt.url)
			require.NoError(t, err)
			assert.Equal(t, tt.dial, opts.DialTimeout)
			assert.Equal(t, tt.read, opts.ReadTimeout)
			assert.Equal(t, tt.write, opts.WriteTimeout)
			assert.True(t, opts.ContextTimeoutEnabled)
			assert.NotNil(t, opts.Dialer)
		})
	}
}

// unresponsiveRedis accepts connections and never replies, like a Redis that
// is partitioned or wedged after the TCP handshake.
func unresponsiveRedis(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	return ln.Addr().String()
}

func TestRedisStoreGetTimesOutAgainstUnresponsiveServer(t *testing.T) {
	opts, err := redisOptions("redis://" + unresponsiveRedis(t))
	require.NoError(t, err)
	store := &RedisStore{client: redis.NewClient(opts), getTimeout: 100 * time.Millisecond}
	t.Cleanup(func() { _ = store.Close() })

	start := time.Now()
	data, err := store.Get(context.Background(), "key")
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Nil(t, data)
	assert.Less(t, elapsed, time.Second)
}

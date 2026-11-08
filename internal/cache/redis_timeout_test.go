package cache

import (
	"bufio"
	"context"
	"net"
	"strconv"
	"strings"
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
	// Socket timeouts well above the assertion bound, so only the Get budget
	// can make the lookup return in time.
	opts, err := redisOptions("redis://" + unresponsiveRedis(t) + "?read_timeout=5s&write_timeout=5s")
	require.NoError(t, err)
	store := &RedisStore{client: redis.NewClient(opts), getTimeout: 100 * time.Millisecond}
	t.Cleanup(func() { _ = store.Close() })

	start := time.Now()
	data, err := store.Get(context.Background(), "key")
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Nil(t, data)
	assert.Less(t, elapsed, 300*time.Millisecond)
}

// slowRedis answers every command after delay: PING with PONG, anything else
// with an error, so the client falls back to a plain RESP2 handshake.
func slowRedis(t *testing.T, delay time.Duration) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var mu sync.Mutex
	var conns []net.Conn
	done := make(chan struct{})
	t.Cleanup(func() {
		close(done)
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
			go serveSlowRedis(c, delay, done)
		}
	}()
	return ln.Addr().String()
}

func serveSlowRedis(c net.Conn, delay time.Duration, done <-chan struct{}) {
	r := bufio.NewReader(c)
	for {
		args, err := readRESPArray(r)
		if err != nil {
			return
		}
		select {
		case <-time.After(delay):
		case <-done:
			return
		}
		reply := "-ERR unknown command\r\n"
		if len(args) > 0 && strings.EqualFold(args[0], "PING") {
			reply = "+PONG\r\n"
		}
		if _, err := c.Write([]byte(reply)); err != nil {
			return
		}
	}
}

func readRESPArray(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "*")))
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, n)
	for range n {
		if _, err := r.ReadString('\n'); err != nil { // $len
			return nil, err
		}
		arg, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		args = append(args, strings.TrimSpace(arg))
	}
	return args, nil
}

func TestNewRedisStoreToleratesSlowStartupPing(t *testing.T) {
	// Each reply takes longer than the 500 ms runtime read timeout but the
	// whole handshake fits the startup budget.
	store, err := NewRedisStore(RedisStoreConfig{URL: "redis://" + slowRedis(t, 600*time.Millisecond)})
	require.NoError(t, err)
	require.NoError(t, store.Close())
}

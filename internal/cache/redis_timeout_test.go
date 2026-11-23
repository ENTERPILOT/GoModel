package cache

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
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
			write: -1, // write follows a read timeout set alone
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

func TestRedisStoreGetTimesOutAgainstUnresponsiveServer(t *testing.T) {
	// Socket timeouts well above the assertion bound, so only the Get budget
	// can make the lookup return in time.
	server := newHangingRedis(t, 0)
	server.hang.Store(true)
	opts, err := redisOptions(server.url() + "?read_timeout=5s&write_timeout=5s")
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

func TestNewRedisStoreToleratesSlowStartupPing(t *testing.T) {
	// Each reply takes longer than the 500 ms runtime read timeout but the
	// whole handshake fits the startup budget.
	store, err := NewRedisStore(RedisStoreConfig{URL: newHangingRedis(t, 600*time.Millisecond).url()})
	require.NoError(t, err)
	require.NoError(t, store.Close())
}

// hangingRedis is a minimal RESP server: it answers PING and refuses every
// other command, each after delay, until hang is set, after which it reads
// commands and never replies, like a Redis that stopped responding without
// closing connections.
type hangingRedis struct {
	ln    net.Listener
	delay time.Duration
	done  chan struct{}
	hang  atomic.Bool
}

func newHangingRedis(t *testing.T, delay time.Duration) *hangingRedis {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := &hangingRedis{ln: ln, delay: delay, done: make(chan struct{})}
	t.Cleanup(func() {
		close(s.done)
		_ = ln.Close()
	})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return s
}

func (s *hangingRedis) url() string { return "redis://" + s.ln.Addr().String() }

func (s *hangingRedis) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	for {
		cmd, err := readRESPCommand(r)
		if err != nil {
			return
		}
		if s.hang.Load() {
			continue
		}
		select {
		case <-time.After(s.delay):
		case <-s.done:
			return
		}
		reply := "-ERR unknown command\r\n"
		if strings.EqualFold(cmd, "PING") {
			reply = "+PONG\r\n"
		}
		if _, err := conn.Write([]byte(reply)); err != nil {
			return
		}
	}
}

// readRESPCommand reads one RESP array of bulk strings and returns its first
// element, the command name.
func readRESPCommand(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(line, "*") {
		return "", fmt.Errorf("unexpected %q", line)
	}
	n, err := strconv.Atoi(strings.TrimSpace(line[1:]))
	if err != nil {
		return "", err
	}
	var name string
	for i := range n {
		header, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		size, err := strconv.Atoi(strings.TrimSpace(header[1:]))
		if err != nil {
			return "", err
		}
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		if i == 0 {
			name = string(buf[:size])
		}
	}
	return name, nil
}

func TestRedisStoreHonorsContextDeadlineWhenRedisHangs(t *testing.T) {
	server := newHangingRedis(t, 0)
	store, err := NewRedisStore(RedisStoreConfig{URL: server.url()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	server.hang.Store(true)

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	require.Error(t, store.Ping(ctx))
	assert.Less(t, time.Since(start), 900*time.Millisecond, "a probe deadline must end the call, not go-redis's read timeout")

	start = time.Now()
	_, err = store.Get(context.Background(), "key")
	require.Error(t, err)
	// go-redis can retry a timed-out read on a fresh connection, so allow
	// two read timeouts; the 5 s go-redis default would take twice as long.
	assert.Less(t, time.Since(start), 2800*time.Millisecond, "without a deadline the shortened read timeout ends the call")
}

func TestApplyRedisTimeoutsKeepsURLTimeouts(t *testing.T) {
	opts, err := redis.ParseURL("redis://localhost:6379?read_timeout=5s&write_timeout=-1")
	require.NoError(t, err)
	applyRedisTimeouts(opts)
	assert.True(t, opts.ContextTimeoutEnabled)
	assert.Equal(t, 5*time.Second, opts.ReadTimeout)
	assert.Equal(t, time.Duration(-1), opts.WriteTimeout)

	opts, err = redis.ParseURL("redis://localhost:6379")
	require.NoError(t, err)
	applyRedisTimeouts(opts)
	assert.Equal(t, defaultRedisIOTimeout, opts.ReadTimeout)
	assert.Equal(t, defaultRedisIOTimeout, opts.WriteTimeout)

	opts, err = redis.ParseURL("redis://localhost:6379?read_timeout=10s")
	require.NoError(t, err)
	applyRedisTimeouts(opts)
	assert.Equal(t, 10*time.Second, opts.WriteTimeout, "write follows a read timeout set alone")
}

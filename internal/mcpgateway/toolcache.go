package mcpgateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/cache"
)

// Tool-result caching. Tool calls are not idempotent in general, so a server
// opts in with a TTL and nothing is cached otherwise. An entry is keyed by
// the server, the credentials the call is made with, the tool, and its
// arguments. Every caller of a server shares its one upstream session and
// credentials (see upstream), so an entry holds nothing caller-specific; who
// may call the tool is still decided on every call, before the cache is read.

const (
	// toolCacheKeyPrefix is the default Redis key prefix of cached results.
	toolCacheKeyPrefix = "gomodel:mcp:"
	// toolCacheRedisTTL expires an entry stored without a TTL of its own.
	// Every entry carries its server's TTL, so this is only a backstop.
	toolCacheRedisTTL = time.Hour
	// toolCacheMemoryBytes bounds the in-memory store used without Redis.
	toolCacheMemoryBytes = 64 << 20
	// maxCachedToolResultBytes is the largest encoded result that is cached;
	// a larger one is relayed and fetched again on the next call.
	maxCachedToolResultBytes = 1 << 20
	// toolCacheOpTimeout bounds one cache read or write, so a store that
	// stops answering costs a tool call about this much.
	toolCacheOpTimeout = time.Second
)

// newToolCacheStore opens the store for cached tool results: Redis when a URL
// is configured, so replicas share entries, and bounded process memory
// otherwise. Like the model cache, an unreachable Redis falls back to memory
// instead of failing startup.
func newToolCacheStore(cfg config.MCPCacheConfig) cache.Store {
	if cfg.Redis.URL == "" {
		slog.Info("mcp tool cache enabled", "store", "memory")
		return cache.NewMemoryStore(toolCacheMemoryBytes)
	}
	prefix := cfg.Redis.Key
	if prefix == "" {
		prefix = toolCacheKeyPrefix
	}
	store, err := cache.NewRedisStore(cache.RedisStoreConfig{URL: cfg.Redis.URL, Prefix: prefix, TTL: toolCacheRedisTTL})
	if err != nil {
		slog.Warn("mcp tool cache: redis is unavailable; caching in memory on this instance", "error", err)
		return cache.NewMemoryStore(toolCacheMemoryBytes)
	}
	slog.Info("mcp tool cache enabled", "store", "redis", "prefix", prefix)
	return store
}

// cachedCall is one tools/call whose result may be cached.
type cachedCall struct {
	key string
	// args are the canonical arguments the key was derived from. They are
	// what is sent upstream, so a stored result always answers its key.
	args json.RawMessage
	ttl  time.Duration
}

// cachedCall returns the cache entry of a call to name, and whether the
// server's cache policy covers it: the server caches, the tool is still
// exposed, and it is one of the listed tools or, with none listed, one the
// upstream annotates as read-only.
func (u *upstream) cachedCall(name string, args json.RawMessage) (cachedCall, bool) {
	spec, ok := u.cachingSpec(name)
	if !ok {
		return cachedCall{}, false
	}
	canonical, ok := canonicalArguments(args)
	if !ok {
		return cachedCall{}, false
	}
	return cachedCall{key: toolCacheKey(spec, name, canonical), args: canonical, ttl: spec.CacheTTL}, true
}

// cachingSpec returns the server's spec when its cache policy covers name.
func (u *upstream) cachingSpec(name string) (ServerSpec, bool) {
	u.stateMu.Lock()
	defer u.stateMu.Unlock()
	spec := u.spec
	if spec.CacheTTL <= 0 || !spec.Enabled || !toolAllowed(name, spec.AllowedTools, spec.DisallowedTools) {
		return ServerSpec{}, false
	}
	if len(spec.CacheTools) > 0 {
		return spec, slices.Contains(spec.CacheTools, name)
	}
	if u.catalog == nil {
		return ServerSpec{}, false
	}
	for _, tool := range u.catalog.discovered {
		if tool.Name == name {
			return spec, tool.Annotations != nil && tool.Annotations.ReadOnlyHint
		}
	}
	return ServerSpec{}, false
}

// canonicalArguments re-encodes tools/call arguments with sorted object keys,
// so calls that differ only in key order or whitespace share an entry.
// Numbers keep their text. It reports false for arguments that are not one
// JSON value, which are relayed uncached.
func canonicalArguments(args json.RawMessage) (json.RawMessage, bool) {
	if len(bytes.TrimSpace(args)) == 0 {
		return nil, true
	}
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false
	}
	var canonical bytes.Buffer
	encoder := json.NewEncoder(&canonical)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, false
	}
	return bytes.TrimSuffix(canonical.Bytes(), []byte("\n")), true
}

// toolCacheKey digests everything a result depends on. The connection and
// its credentials are part of it, so a server pointed at another endpoint,
// given another token, or signed in again starts from an empty cache instead
// of serving what the previous account could see. The digest is one-way: no
// credential can be read back from a key.
func toolCacheKey(spec ServerSpec, tool string, args []byte) string {
	digest := sha256.New()
	// Each list is written as its length followed by length-prefixed items,
	// so no two inputs produce the same byte stream.
	write := func(values ...string) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(values)))
		digest.Write(size[:])
		for _, value := range values {
			binary.BigEndian.PutUint64(size[:], uint64(len(value)))
			digest.Write(size[:])
			digest.Write([]byte(value))
		}
	}
	writeMap := func(values map[string]string) {
		pairs := make([]string, 0, 2*len(values))
		for _, key := range slices.Sorted(maps.Keys(values)) {
			pairs = append(pairs, key, values[key])
		}
		write(pairs...)
	}

	write("v1", spec.Name, spec.URL, spec.Transport, spec.Command)
	write(spec.Args...)
	writeMap(spec.Headers)
	writeMap(spec.Env)
	if spec.oauth != nil {
		record := spec.oauth.snapshot()
		write(record.ClientID, strconv.FormatInt(record.SignedInAt.UnixNano(), 10))
	} else {
		write()
	}
	write(tool, string(args))
	return hex.EncodeToString(digest.Sum(nil))
}

// toolCache stores successful tool results in a cache.Store.
type toolCache struct {
	store cache.Store
}

// get returns the cached result under key, or nil. A store failure is a miss:
// the call goes upstream as if nothing were cached.
func (c *toolCache) get(ctx context.Context, key string) *mcp.CallToolResult {
	ctx, cancel := context.WithTimeout(ctx, toolCacheOpTimeout)
	defer cancel()
	data, err := c.store.Get(ctx, key)
	if err != nil {
		slog.Warn("mcp tool cache read failed", "error", err)
		return nil
	}
	if len(data) == 0 {
		return nil
	}
	var result mcp.CallToolResult
	if err := json.Unmarshal(data, &result); err != nil {
		slog.Warn("mcp tool cache entry is unreadable", "error", err)
		return nil
	}
	return &result
}

// put stores a completed, successful result. Tool errors and results asking
// the client for input are never cached: the next call must reach the server.
func (c *toolCache) put(ctx context.Context, call cachedCall, result *mcp.CallToolResult) {
	if result == nil || result.IsError || result.NeedsInput() {
		return
	}
	data, err := json.Marshal(result)
	if err != nil || len(data) > maxCachedToolResultBytes {
		return
	}
	// The result is already in hand, so a caller that went away does not
	// stop it from being stored.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), toolCacheOpTimeout)
	defer cancel()
	if err := c.store.Set(ctx, call.key, data, call.ttl); err != nil {
		slog.Warn("mcp tool cache write failed", "error", err)
	}
}

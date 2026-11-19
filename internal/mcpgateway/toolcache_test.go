package mcpgateway

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/cache"
	"github.com/enterpilot/gomodel/internal/usage"
)

// addCountingTool registers a tool that answers with its call number and the
// arguments it received, so a test can tell a relayed call from a cached one.
func addCountingTool(name string, readOnly bool, calls *atomic.Int32) func(*mcp.Server) {
	return func(server *mcp.Server) {
		server.AddTool(&mcp.Tool{
			Name:        name,
			InputSchema: map[string]any{"type": "object"},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly},
		}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			n := calls.Add(1)
			return &mcp.CallToolResult{
				Content:           []mcp.Content{&mcp.TextContent{Text: string(req.Params.Arguments)}},
				StructuredContent: map[string]any{"call": n},
			}, nil
		})
	}
}

func cachingSpec(name, url string, tools ...string) ServerSpec {
	return testSpec(name, url, func(spec *ServerSpec) {
		spec.CacheTTL = time.Minute
		spec.CacheTools = tools
	})
}

func newCachingService(t *testing.T, usageLog usage.LoggerInterface, store cache.Store, specs ...ServerSpec) (*Service, string) {
	t.Helper()
	servers := make(map[string]ServerSpec, len(specs))
	for _, spec := range specs {
		servers[spec.Name] = spec
	}
	return newTestServiceWithOptions(t, Options{ConfigServers: servers, UsageLogger: usageLog, ToolCache: store})
}

func resultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	return text.Text
}

func TestToolCacheServesRepeatedCallFromCache(t *testing.T) {
	var calls atomic.Int32
	url := newTestUpstream(t, "docs", addCountingTool("search", true, &calls))
	usageLog := &recordingUsageLogger{}
	_, gatewayURL := newCachingService(t, usageLog, cache.NewMemoryStore(1<<20), cachingSpec("docs", url))
	ctx := context.Background()

	alice := connectClient(t, gatewayURL+"/mcp", map[string]string{"X-Test-Auth-Key-ID": "alice"})
	first, err := alice.CallTool(ctx, &mcp.CallToolParams{Name: "docs_search", Arguments: json.RawMessage(`{"q":"go","limit":5}`)})
	require.NoError(t, err)

	// Another key on its own session, with the same arguments in another
	// order: one shared upstream session answers both, so one entry does too.
	bob := connectClient(t, gatewayURL+"/mcp", map[string]string{"X-Test-Auth-Key-ID": "bob"})
	second, err := bob.CallTool(ctx, &mcp.CallToolParams{Name: "docs_search", Arguments: json.RawMessage(`{ "limit": 5, "q": "go" }`)})
	require.NoError(t, err)

	assert.Equal(t, int32(1), calls.Load(), "the repeated call must not reach the upstream")
	assert.Equal(t, `{"limit":5,"q":"go"}`, resultText(t, first), "the upstream receives the canonical arguments the entry is keyed by")
	wantWire, err := json.Marshal(first)
	require.NoError(t, err)
	gotWire, err := json.Marshal(second)
	require.NoError(t, err)
	assert.JSONEq(t, string(wantWire), string(gotWire), "a cached result must reach the client unchanged")

	other, err := bob.CallTool(ctx, &mcp.CallToolParams{Name: "docs_search", Arguments: json.RawMessage(`{"q":"rust","limit":5}`)})
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load(), "other arguments are another entry")
	assert.Equal(t, `{"limit":5,"q":"rust"}`, resultText(t, other))

	require.Eventually(t, func() bool { return len(usageLog.all()) == 3 }, 2*time.Second, 10*time.Millisecond)
	entries := usageLog.all()
	assert.Empty(t, entries[0].CacheType)
	assert.Equal(t, usage.CacheTypeExact, entries[1].CacheType, "a cache hit is still a usage entry, marked as cached")
	assert.Equal(t, "bob", entries[1].AuthKeyID)
	assert.Equal(t, "docs_search", entries[1].Model)
	assert.Empty(t, entries[2].CacheType)
}

func TestToolCacheIsOffByDefault(t *testing.T) {
	var calls atomic.Int32
	url := newTestUpstream(t, "docs", addCountingTool("search", true, &calls))
	service, _ := newCachingService(t, nil, cache.NewMemoryStore(1<<20), testSpec("docs", url, nil))

	for range 2 {
		_, cached, err := service.manager.CallTool(context.Background(), "docs", "search", json.RawMessage(`{"q":"go"}`))
		require.NoError(t, err)
		assert.False(t, cached)
	}
	assert.Equal(t, int32(2), calls.Load(), "a server that did not opt in is never cached")
}

func TestToolCacheEligibleTools(t *testing.T) {
	tests := []struct {
		name       string
		cacheTools []string
		tool       string
		wantCached bool
	}{
		{name: "read-only hint is cached by default", tool: "search", wantCached: true},
		{name: "tool without the hint is not", tool: "create", wantCached: false},
		{name: "listed tool is cached without the hint", cacheTools: []string{"create"}, tool: "create", wantCached: true},
		{name: "a list replaces the hint", cacheTools: []string{"create"}, tool: "search", wantCached: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			url := newTestUpstream(t, "docs", func(server *mcp.Server) {
				addCountingTool("search", true, &calls)(server)
				addCountingTool("create", false, &calls)(server)
			})
			service, _ := newCachingService(t, nil, cache.NewMemoryStore(1<<20), cachingSpec("docs", url, tt.cacheTools...))

			_, cached, err := service.manager.CallTool(context.Background(), "docs", tt.tool, json.RawMessage(`{}`))
			require.NoError(t, err)
			assert.False(t, cached, "the first call always reaches the upstream")
			_, cached, err = service.manager.CallTool(context.Background(), "docs", tt.tool, json.RawMessage(`{}`))
			require.NoError(t, err)
			assert.Equal(t, tt.wantCached, cached)
		})
	}
}

func TestToolCacheDoesNotCacheToolErrors(t *testing.T) {
	var calls atomic.Int32
	url := newTestUpstream(t, "docs", func(server *mcp.Server) {
		server.AddTool(&mcp.Tool{Name: "flaky", InputSchema: map[string]any{"type": "object"}},
			func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				if calls.Add(1) == 1 {
					return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "try again"}}}, nil
				}
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
			})
	})
	service, _ := newCachingService(t, nil, cache.NewMemoryStore(1<<20), cachingSpec("docs", url, "flaky"))
	ctx := context.Background()

	failed, cached, err := service.manager.CallTool(ctx, "docs", "flaky", nil)
	require.NoError(t, err)
	require.True(t, failed.IsError)
	assert.False(t, cached)

	recovered, cached, err := service.manager.CallTool(ctx, "docs", "flaky", nil)
	require.NoError(t, err)
	assert.False(t, cached, "a tool error must not be served to the next call")
	assert.Equal(t, "ok", resultText(t, recovered))

	_, cached, err = service.manager.CallTool(ctx, "docs", "flaky", nil)
	require.NoError(t, err)
	assert.True(t, cached)
	assert.Equal(t, int32(2), calls.Load())
}

// A cached result must not outlive the access rules: a tool the operator
// hides after it was cached fails like any other excluded tool.
func TestToolCacheHitRespectsToolFilters(t *testing.T) {
	var calls atomic.Int32
	url := newTestUpstream(t, "docs", addCountingTool("search", true, &calls))
	spec := cachingSpec("docs", url)
	service, _ := newCachingService(t, nil, cache.NewMemoryStore(1<<20), spec)
	ctx := context.Background()

	_, _, err := service.manager.CallTool(ctx, "docs", "search", nil)
	require.NoError(t, err)
	_, cached, err := service.manager.CallTool(ctx, "docs", "search", nil)
	require.NoError(t, err)
	require.True(t, cached)

	spec.DisallowedTools = []string{"search"}
	service.manager.Apply([]ServerSpec{spec})

	_, cached, err = service.manager.CallTool(ctx, "docs", "search", nil)
	require.ErrorIs(t, err, ErrToolExcluded)
	assert.False(t, cached)
}

// failingStore is a cache backend that is down.
type failingStore struct{}

func (failingStore) Get(context.Context, string) ([]byte, error) {
	return nil, errors.New("store down")
}
func (failingStore) Set(context.Context, string, []byte, time.Duration) error {
	return errors.New("store down")
}
func (failingStore) Close() error { return nil }

func TestToolCacheStoreFailureRelaysTheCall(t *testing.T) {
	var calls atomic.Int32
	url := newTestUpstream(t, "docs", addCountingTool("search", true, &calls))
	service, _ := newCachingService(t, nil, failingStore{}, cachingSpec("docs", url))

	for range 2 {
		result, cached, err := service.manager.CallTool(context.Background(), "docs", "search", json.RawMessage(`{"q":"go"}`))
		require.NoError(t, err)
		assert.False(t, cached)
		assert.Equal(t, `{"q":"go"}`, resultText(t, result))
	}
	assert.Equal(t, int32(2), calls.Load())
}

// recordingStore remembers what was stored and for how long.
type recordingStore struct {
	mu   sync.Mutex
	ttls map[string]time.Duration
	cache.Store
}

func (s *recordingStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	s.mu.Lock()
	s.ttls[key] = ttl
	s.mu.Unlock()
	return s.Store.Set(ctx, key, value, ttl)
}

func TestToolCachePutStoresOnlyBoundedSuccessfulResults(t *testing.T) {
	store := &recordingStore{ttls: map[string]time.Duration{}, Store: cache.NewMapStore()}
	toolCache := &toolCache{store: store}
	ctx := context.Background()
	text := func(s string) []mcp.Content { return []mcp.Content{&mcp.TextContent{Text: s}} }

	toolCache.put(ctx, cachedCall{key: "nil", ttl: time.Minute}, nil)
	toolCache.put(ctx, cachedCall{key: "error", ttl: time.Minute}, &mcp.CallToolResult{IsError: true, Content: text("boom")})
	toolCache.put(ctx, cachedCall{key: "huge", ttl: time.Minute}, &mcp.CallToolResult{Content: text(strings.Repeat("x", maxCachedToolResultBytes))})
	toolCache.put(ctx, cachedCall{key: "ok", ttl: 90 * time.Second}, &mcp.CallToolResult{Content: text("fine")})

	assert.Equal(t, map[string]time.Duration{"ok": 90 * time.Second}, store.ttls, "only the successful result within the size bound is stored, for the server's TTL")
	hit := toolCache.get(ctx, "ok")
	require.NotNil(t, hit)
	assert.Equal(t, "fine", resultText(t, hit))
	assert.Nil(t, toolCache.get(ctx, "error"))

	require.NoError(t, store.Store.Set(ctx, "garbage", []byte("not json"), time.Minute))
	assert.Nil(t, toolCache.get(ctx, "garbage"), "an unreadable entry is a miss")
}

func TestCanonicalArguments(t *testing.T) {
	tests := []struct {
		name   string
		args   string
		want   string
		wantOK bool
	}{
		{name: "absent", args: "", want: "", wantOK: true},
		{name: "blank", args: " \n", want: "", wantOK: true},
		{name: "sorts keys at every depth", args: `{"b":{"z":1,"a":[{"y":2,"x":1}]},"a":true}`, want: `{"a":true,"b":{"a":[{"x":1,"y":2}],"z":1}}`, wantOK: true},
		{name: "keeps array order", args: `{"ids":[3,1,2]}`, want: `{"ids":[3,1,2]}`, wantOK: true},
		{name: "keeps number text", args: `{"n":1.0,"big":12345678901234567890}`, want: `{"big":12345678901234567890,"n":1.0}`, wantOK: true},
		{name: "does not escape html", args: `{"q":"a<b && c>d"}`, want: `{"q":"a<b && c>d"}`, wantOK: true},
		{name: "last duplicate key wins", args: `{"q":"first","q":"last"}`, want: `{"q":"last"}`, wantOK: true},
		{name: "invalid json", args: `{"q":`, wantOK: false},
		{name: "trailing value", args: `{"q":1} {"q":2}`, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := canonicalArguments(json.RawMessage(tt.args))
			require.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

func TestToolCacheKeyCoversServerCredentialsToolAndArguments(t *testing.T) {
	base := func() ServerSpec {
		return ServerSpec{
			Name:      "docs",
			URL:       "https://docs.example/mcp",
			Transport: config.MCPTransportHTTP,
			Headers:   map[string]string{"Authorization": "Bearer one", "X-Team": "a"},
		}
	}
	args := []byte(`{"q":"go"}`)
	key := toolCacheKey(base(), "search", args)

	assert.Equal(t, key, toolCacheKey(base(), "search", args), "the key is stable, so replicas and restarts share entries")
	assert.Len(t, key, 64)
	assert.NotContains(t, key, "one", "the key is a digest, never the credential")

	signIn := func(clientID string, at time.Time) *oauthSource {
		return newOAuthSource("docs", oauthRecord{ClientID: clientID, SignedInAt: at, AccessToken: "token"}, nil, nil)
	}
	signedInAt := time.Unix(1_700_000_000, 0)
	withSignIn := func(source *oauthSource) string {
		spec := base()
		spec.oauth = source
		return toolCacheKey(spec, "search", args)
	}
	refreshed := signIn("client-a", signedInAt)
	refreshed.adopt(oauthRecord{ClientID: "client-a", SignedInAt: signedInAt, AccessToken: "rotated", UpdatedAt: signedInAt.Add(time.Minute)})
	require.Equal(t, "rotated", refreshed.snapshot().AccessToken)
	assert.Equal(t, withSignIn(signIn("client-a", signedInAt)), withSignIn(refreshed), "a token refresh keeps the sign-in's entries")

	different := map[string]string{
		"another tool":      toolCacheKey(base(), "fetch", args),
		"other arguments":   toolCacheKey(base(), "search", []byte(`{"q":"rust"}`)),
		"no arguments":      toolCacheKey(base(), "search", nil),
		"another server":    toolCacheKey(testSpecWith(base(), func(s *ServerSpec) { s.Name = "wiki" }), "search", args),
		"another url":       toolCacheKey(testSpecWith(base(), func(s *ServerSpec) { s.URL = "https://other.example/mcp" }), "search", args),
		"another token":     toolCacheKey(testSpecWith(base(), func(s *ServerSpec) { s.Headers["Authorization"] = "Bearer two" }), "search", args),
		"moved header":      toolCacheKey(testSpecWith(base(), func(s *ServerSpec) { s.Headers = map[string]string{"Authorization": "Bearer one", "X-Team a": ""} }), "search", args),
		"stdio environment": toolCacheKey(testSpecWith(base(), func(s *ServerSpec) { s.Env = map[string]string{"TOKEN": "x"} }), "search", args),
		"oauth sign-in":     withSignIn(signIn("client-a", signedInAt)),
		"a later sign-in":   withSignIn(signIn("client-a", signedInAt.Add(time.Hour))),
		"another client":    withSignIn(signIn("client-b", signedInAt)),
		"tool/args framing": toolCacheKey(base(), "search"+string(args), nil),
	}
	seen := map[string]string{key: "base"}
	for name, other := range different {
		previous, dup := seen[other]
		assert.False(t, dup, "%s shares a key with %s", name, previous)
		seen[other] = name
	}
}

func testSpecWith(spec ServerSpec, mutate func(*ServerSpec)) ServerSpec {
	mutate(&spec)
	return spec
}

func TestNewToolCacheStoreDefaultsToMemory(t *testing.T) {
	store := newToolCacheStore(config.MCPCacheConfig{})
	assert.IsType(t, &cache.MemoryStore{}, store, "without Redis, results are cached in process memory")

	// A Redis that cannot be used must not stop the gateway from starting.
	store = newToolCacheStore(config.MCPCacheConfig{Redis: config.RedisMCPConfig{URL: "not-a-redis-url"}})
	assert.IsType(t, &cache.MemoryStore{}, store)
}

package mcpgateway

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/sharedstore"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

const listToolsBody = `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`

// runSharedStoreSuite runs body against the shared store on every storage
// backend: SQLite always, PostgreSQL and MongoDB when their test databases
// are configured.
func runSharedStoreSuite(t *testing.T, body func(t *testing.T, store ext.SharedStore)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		shared, err := storage.NewSQLite(storage.SQLiteConfig{Path: filepath.Join(t.TempDir(), "gomodel.db")})
		require.NoError(t, err)
		t.Cleanup(func() { _ = shared.Close() })
		body(t, openSharedStore(t, shared))
	})
	t.Run("postgresql", func(t *testing.T) {
		pool := sqlxtest.NewPostgresPool(t)
		if pool == nil {
			return // NewPostgresPool already skipped
		}
		body(t, openSharedStore(t, postgresStorage{pool: pool}))
	})
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		body(t, openSharedStore(t, mongoStorage{db: db}))
	})
}

func openSharedStore(t *testing.T, shared storage.Storage) ext.SharedStore {
	t.Helper()
	store, err := sharedstore.New(context.Background(), shared)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// postgresStorage and mongoStorage hand a test database to the shared store;
// the test helpers that opened it close it.
type postgresStorage struct{ pool *pgxpool.Pool }

func (postgresStorage) Close() error          { return nil }
func (s postgresStorage) Pool() *pgxpool.Pool { return s.pool }

type mongoStorage struct{ db *mongo.Database }

func (mongoStorage) Close() error                { return nil }
func (s mongoStorage) Database() *mongo.Database { return s.db }

// testClock runs ahead of the wall clock by a settable offset, so rechecks
// and expiry are reached without sleeping.
type testClock struct{ offset atomic.Int64 }

func (c *testClock) Now() time.Time { return time.Now().Add(time.Duration(c.offset.Load())) }

func (c *testClock) Advance(d time.Duration) { c.offset.Add(int64(d)) }

// memorySharedStore is an ext.SharedStore whose expiry follows a test clock.
type memorySharedStore struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]ext.SharedEntry
}

func newMemorySharedStore(now func() time.Time) *memorySharedStore {
	return &memorySharedStore{now: now, entries: map[string]ext.SharedEntry{}}
}

func (m *memorySharedStore) Put(_ context.Context, namespace, key string, value []byte, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !expiresAt.IsZero() && !expiresAt.After(m.now()) {
		delete(m.entries, namespace+"/"+key)
		return nil
	}
	m.entries[namespace+"/"+key] = ext.SharedEntry{Key: key, Value: value, ExpiresAt: expiresAt}
	return nil
}

func (m *memorySharedStore) Get(_ context.Context, namespace, key string) ([]byte, bool, error) {
	entry, ok := m.entry(namespace, key)
	return entry.Value, ok, nil
}

func (m *memorySharedStore) Delete(_ context.Context, namespace, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, namespace+"/"+key)
	return nil
}

func (m *memorySharedStore) List(_ context.Context, namespace string) ([]ext.SharedEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var entries []ext.SharedEntry
	for id, entry := range m.entries {
		if strings.HasPrefix(id, namespace+"/") && (entry.ExpiresAt.IsZero() || entry.ExpiresAt.After(m.now())) {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func (m *memorySharedStore) entry(namespace, key string) (ext.SharedEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[namespace+"/"+key]
	if !ok || (!entry.ExpiresAt.IsZero() && !entry.ExpiresAt.After(m.now())) {
		return ext.SharedEntry{}, false
	}
	return entry, true
}

// replica is one gateway instance of a test cluster.
type replica struct {
	service *Service
	url     string
	clock   *testClock
}

func newReplica(t *testing.T, store ext.SharedStore, opts Options, clock *testClock) replica {
	t.Helper()
	opts.SharedStore = store
	service, url := newTestServiceWithOptions(t, opts)
	if clock == nil {
		clock = &testClock{}
	}
	service.now = clock.Now
	return replica{service: service, url: url, clock: clock}
}

func (r replica) binding(sessionID string) (sessionBinding, bool) {
	r.service.bindMu.Lock()
	defer r.service.bindMu.Unlock()
	binding, ok := r.service.bindings[sessionID]
	return binding, ok
}

func alphaServers(t *testing.T) map[string]ServerSpec {
	t.Helper()
	return map[string]ServerSpec{"alpha": testSpec("alpha", newTestUpstream(t, "alpha", addEchoTool("echo")), nil)}
}

func rawMCPDelete(t *testing.T, endpoint string, headers map[string]string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, endpoint, nil)
	require.NoError(t, err)

	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer resp.Body.Close()
	return resp.StatusCode
}

func withSession(headers map[string]string, sessionID string) map[string]string {
	merged := map[string]string{"Mcp-Session-Id": sessionID}
	maps.Copy(merged, headers)
	return merged
}

// A client opens its session on one replica, then the load balancer sends
// its requests to another: they carry on with the scope it opened with.
func TestSharedSessionContinuesOnAnotherReplica(t *testing.T) {
	runSharedStoreSuite(t, func(t *testing.T, store ext.SharedStore) {
		servers := map[string]ServerSpec{
			"alpha": testSpec("alpha", newTestUpstream(t, "alpha", addEchoTool("echo")), nil),
			"beta":  testSpec("beta", newTestUpstream(t, "beta", addEchoTool("search")), nil),
		}
		a := newReplica(t, store, Options{ConfigServers: servers}, nil)
		b := newReplica(t, store, Options{ConfigServers: servers}, nil)

		var target atomic.Pointer[Service]
		target.Store(a.service)
		balancer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gatewayHandler(target.Load()).ServeHTTP(w, r)
		}))
		t.Cleanup(balancer.Close)

		session := connectClient(t, balancer.URL+"/mcp", map[string]string{
			ScopeHeader:          "alpha",
			"X-Test-Auth-Key-ID": "key-a",
		})
		target.Store(b.service)

		echo := NamespacedName("alpha", "echo")
		assert.Equal(t, []string{echo}, listToolNames(t, session), "the scope the session opened with holds on the other replica")
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: echo, Arguments: map[string]any{"q": "hi"}})
		require.NoError(t, err)
		require.Len(t, result.Content, 1)
		assert.Equal(t, `echo:{"q":"hi"}`, result.Content[0].(*mcp.TextContent).Text)

		binding, ok := b.binding(session.ID())
		require.True(t, ok)
		assert.NotNil(t, binding.resumed, "the other replica hosts the session itself")
		assert.Equal(t, "key-a", binding.authKeyID)
	})
}

// A leaked session ID does not let another principal, or another endpoint,
// ride the session on a replica that did not open it.
func TestSharedSessionStaysBoundToItsPrincipal(t *testing.T) {
	runSharedStoreSuite(t, func(t *testing.T, store ext.SharedStore) {
		servers := alphaServers(t)
		a := newReplica(t, store, Options{ConfigServers: servers}, nil)
		b := newReplica(t, store, Options{ConfigServers: servers}, nil)
		owner := map[string]string{"X-Test-Auth-Key-ID": "key-a", core.UserPathHeader: "/team"}
		sessionID := initializeRawSession(t, a.url+"/mcp", owner)

		foreign := map[string]map[string]string{
			"auth key":  {"X-Test-Auth-Key-ID": "key-b", core.UserPathHeader: "/team"},
			"user path": {"X-Test-Auth-Key-ID": "key-a", core.UserPathHeader: "/other"},
			"no key":    {core.UserPathHeader: "/team"},
		}
		for name, headers := range foreign {
			assert.Equal(t, http.StatusNotFound, rawMCPStatus(t, b.url+"/mcp", listToolsBody, withSession(headers, sessionID)), name)
			assert.Equal(t, http.StatusNotFound, rawMCPDelete(t, b.url+"/mcp", withSession(headers, sessionID)), name)
		}
		assert.Equal(t, http.StatusNotFound, rawMCPStatus(t, b.url+"/mcp/alpha", listToolsBody, withSession(owner, sessionID)), "another endpoint")
		_, resumed := b.binding(sessionID)
		assert.False(t, resumed, "a rejected request resumes nothing")

		assert.Equal(t, http.StatusOK, rawMCPStatus(t, b.url+"/mcp", listToolsBody, withSession(owner, sessionID)))
		assert.Equal(t, http.StatusOK, rawMCPStatus(t, a.url+"/mcp", listToolsBody, withSession(owner, sessionID)))
	})
}

// A DELETE on any replica ends the session on every replica: the one hosting
// it stops within a recheck interval, and none can resume it.
func TestSharedSessionDeleteEndsItOnEveryReplica(t *testing.T) {
	cases := []struct {
		name string
		// resumeOnB has replica B host the session before the DELETE.
		resumeOnB bool
		deleteOnB bool
	}{
		{name: "deleted where it was opened", resumeOnB: true},
		{name: "deleted where it was resumed", resumeOnB: true, deleteOnB: true},
		{name: "deleted where it is not hosted", deleteOnB: true},
	}
	runSharedStoreSuite(t, func(t *testing.T, store ext.SharedStore) {
		servers := alphaServers(t)
		a := newReplica(t, store, Options{ConfigServers: servers}, nil)
		b := newReplica(t, store, Options{ConfigServers: servers}, nil)
		owner := map[string]string{"X-Test-Auth-Key-ID": "key-a"}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				sessionID := initializeRawSession(t, a.url+"/mcp", owner)
				if tc.resumeOnB {
					require.Equal(t, http.StatusOK, rawMCPStatus(t, b.url+"/mcp", listToolsBody, withSession(owner, sessionID)))
				}
				deleter, other := a, b
				if tc.deleteOnB {
					deleter, other = b, a
				}
				require.Equal(t, http.StatusNoContent, rawMCPDelete(t, deleter.url+"/mcp", withSession(owner, sessionID)))
				assert.Equal(t, http.StatusNotFound, rawMCPStatus(t, deleter.url+"/mcp", listToolsBody, withSession(owner, sessionID)))

				other.clock.Advance(sessionRecheckInterval)
				assert.Equal(t, http.StatusNotFound, rawMCPStatus(t, other.url+"/mcp", listToolsBody, withSession(owner, sessionID)))
				_, bound := other.binding(sessionID)
				assert.False(t, bound)
			})
		}
	})
}

// Deleting a virtual server ends its sessions on every replica, before the
// others reload, and a virtual server recreated under the same name does not
// revive them.
func TestDeleteVirtualEndsSharedSessionsOnEveryReplica(t *testing.T) {
	runSharedStoreSuite(t, func(t *testing.T, store ext.SharedStore) {
		ctx := context.Background()
		servers := alphaServers(t)
		virtualStore := &memoryVirtualStore{rows: map[string]ManagedVirtualServer{
			"coding": {Name: "coding", Servers: []string{"alpha"}},
		}}
		options := func() Options {
			return Options{ConfigServers: servers, Store: &memoryStore{rows: map[string]ManagedServer{}}, VirtualStore: virtualStore}
		}
		a := newReplica(t, store, options(), nil)
		b := newReplica(t, store, options(), nil)
		sessionID := initializeRawSession(t, a.url+"/mcp/coding", nil)
		require.Equal(t, http.StatusOK, rawMCPStatus(t, b.url+"/mcp/coding", listToolsBody, withSession(nil, sessionID)))

		require.NoError(t, a.service.DeleteVirtual(ctx, "coding"))
		require.True(t, b.service.IsVirtual("coding"), "replica B has not reloaded yet")
		b.clock.Advance(sessionRecheckInterval)
		assert.Equal(t, http.StatusNotFound, rawMCPStatus(t, b.url+"/mcp/coding", listToolsBody, withSession(nil, sessionID)))
		assert.Equal(t, http.StatusNotFound, rawMCPStatus(t, a.url+"/mcp/coding", listToolsBody, withSession(nil, sessionID)))

		_, err := a.service.UpsertVirtual(ctx, ManagedVirtualServer{Name: "coding", Servers: []string{"alpha"}})
		require.NoError(t, err)
		c := newReplica(t, store, options(), nil)
		assert.Equal(t, http.StatusNotFound, rawMCPStatus(t, c.url+"/mcp/coding", listToolsBody, withSession(nil, sessionID)),
			"the recreated virtual server does not revive the deleted one's session")
		fresh := initializeRawSession(t, a.url+"/mcp/coding", nil)
		assert.Equal(t, http.StatusOK, rawMCPStatus(t, c.url+"/mcp/coding", listToolsBody, withSession(nil, fresh)))
	})
}

// The shared record expires a session idle timeout after its last activity
// on any replica, and activity extends it at most once a touch interval.
func TestSharedSessionExpiresWhenIdle(t *testing.T) {
	clock := &testClock{}
	store := newMemorySharedStore(clock.Now)
	servers := alphaServers(t)
	a := newReplica(t, store, Options{ConfigServers: servers}, clock)
	b := newReplica(t, store, Options{ConfigServers: servers}, clock)
	sessionID := initializeRawSession(t, a.url+"/mcp", nil)
	expiry := func() time.Time {
		entry, ok := store.entry(sharedSessionNamespace, sessionKey(sessionID))
		require.True(t, ok)
		return entry.ExpiresAt
	}
	opened := expiry()
	assert.WithinDuration(t, clock.Now().Add(sessionIdleTimeout), opened, time.Second)

	clock.Advance(sessionTouchInterval / 2)
	require.Equal(t, http.StatusOK, rawMCPStatus(t, a.url+"/mcp", listToolsBody, withSession(nil, sessionID)))
	assert.Equal(t, opened, expiry(), "no write before a touch interval passes")
	clock.Advance(sessionTouchInterval / 2)
	require.Equal(t, http.StatusOK, rawMCPStatus(t, a.url+"/mcp", listToolsBody, withSession(nil, sessionID)))
	touched := expiry()
	assert.WithinDuration(t, clock.Now().Add(sessionIdleTimeout), touched, time.Second)

	clock.Advance(sessionIdleTimeout)
	assert.Equal(t, http.StatusNotFound, rawMCPStatus(t, b.url+"/mcp", listToolsBody, withSession(nil, sessionID)),
		"an idle session cannot be resumed")
	_, ok := store.entry(sharedSessionNamespace, sessionKey(sessionID))
	assert.False(t, ok)
}

// The store keys sessions by a digest: the session ID itself, a bearer
// credential for the session, is never stored.
func TestSharedSessionStoresNoSessionID(t *testing.T) {
	store := newMemorySharedStore(time.Now)
	a := newReplica(t, store, Options{ConfigServers: alphaServers(t)}, nil)
	sessionID := initializeRawSession(t, a.url+"/mcp", map[string]string{"X-Test-Auth-Key-ID": "key-a"})

	entries, err := store.List(context.Background(), sharedSessionNamespace)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.NotContains(t, entries[0].Key, sessionID)
	assert.NotContains(t, string(entries[0].Value), sessionID)
	assert.Contains(t, string(entries[0].Value), `"auth_key_id":"key-a"`)
}

// Without a shared store each replica keeps its own sessions, as before.
func TestSessionsStayLocalWithoutSharedStore(t *testing.T) {
	servers := alphaServers(t)
	a := newReplica(t, nil, Options{ConfigServers: servers}, nil)
	b := newReplica(t, nil, Options{ConfigServers: servers}, nil)
	sessionID := initializeRawSession(t, a.url+"/mcp", nil)

	assert.Equal(t, http.StatusNotFound, rawMCPStatus(t, b.url+"/mcp", listToolsBody, withSession(nil, sessionID)))
	assert.Equal(t, http.StatusOK, rawMCPStatus(t, a.url+"/mcp", listToolsBody, withSession(nil, sessionID)))
}

// A DELETE with an Mcp-Protocol-Version the gateway does not negotiate is
// rejected before it reaches the session, wherever the session is hosted: the
// session stays open, shared, and bound to its principal. A version from the
// per-request protocol on passes the check, as in the SDK handler.
func TestDeleteWithUnsupportedProtocolVersionKeepsTheSession(t *testing.T) {
	store := newMemorySharedStore(time.Now)
	servers := alphaServers(t)
	a := newReplica(t, store, Options{ConfigServers: servers}, nil)
	b := newReplica(t, store, Options{ConfigServers: servers}, nil)
	owner := map[string]string{"X-Test-Auth-Key-ID": "key-a"}
	foreign := map[string]string{"X-Test-Auth-Key-ID": "key-b"}
	withVersion := func(headers map[string]string, sessionID, version string) map[string]string {
		merged := withSession(headers, sessionID)
		merged["Mcp-Protocol-Version"] = version
		return merged
	}

	for name, resumed := range map[string]bool{"sdk-hosted": false, "resumed": true} {
		t.Run(name, func(t *testing.T) {
			sessionID := initializeRawSession(t, a.url+"/mcp", owner)
			host := a
			if resumed {
				host = b
				require.Equal(t, http.StatusOK, rawMCPStatus(t, b.url+"/mcp", listToolsBody, withSession(owner, sessionID)))
			}

			assert.Equal(t, http.StatusBadRequest, rawMCPDelete(t, host.url+"/mcp", withVersion(owner, sessionID, "2024-01-01")))
			_, bound := host.binding(sessionID)
			assert.True(t, bound, "the binding survives a rejected DELETE")
			_, stored := store.entry(sharedSessionNamespace, sessionKey(sessionID))
			assert.True(t, stored, "the shared record survives a rejected DELETE")
			_, closed := store.entry(sharedSessionNamespace, sessionCloseKey(sessionID))
			assert.False(t, closed, "a rejected DELETE writes no close mark")
			assert.Equal(t, http.StatusNotFound, rawMCPStatus(t, host.url+"/mcp", listToolsBody, withSession(foreign, sessionID)))
			assert.Equal(t, http.StatusOK, rawMCPStatus(t, host.url+"/mcp", listToolsBody, withSession(owner, sessionID)))

			assert.Equal(t, http.StatusNoContent, rawMCPDelete(t, host.url+"/mcp", withVersion(owner, sessionID, "2099-01-01")))
		})
	}
}

// countingSharedStore counts the reads and writes that reach a shared store.
// While gate is set, reads wait for it to close.
type countingSharedStore struct {
	ext.SharedStore
	gate       chan struct{}
	gets, puts atomic.Int64
}

func (c *countingSharedStore) Get(ctx context.Context, namespace, key string) ([]byte, bool, error) {
	c.gets.Add(1)
	if c.gate != nil {
		<-c.gate
	}
	return c.SharedStore.Get(ctx, namespace, key)
}

func (c *countingSharedStore) Put(ctx context.Context, namespace, key string, value []byte, expiresAt time.Time) error {
	c.puts.Add(1)
	return c.SharedStore.Put(ctx, namespace, key, value, expiresAt)
}

// Concurrent requests for one session share its due recheck and touch: one
// reads the close mark and one extends the expiry, not each of them.
func TestConcurrentRequestsShareOneRecheck(t *testing.T) {
	const requests = 8
	clock := &testClock{}
	store := &countingSharedStore{SharedStore: newMemorySharedStore(clock.Now)}
	a := newReplica(t, store, Options{ConfigServers: alphaServers(t)}, clock)
	sessionID := initializeRawSession(t, a.url+"/mcp", nil)
	clock.Advance(sessionTouchInterval)
	store.gets.Store(0)
	store.puts.Store(0)
	// The store holds the first recheck open; the requests that share it
	// finish meanwhile.
	store.gate = make(chan struct{})

	finished := make(chan struct{}, requests)
	var wg sync.WaitGroup
	for id := range requests {
		// Distinct JSON-RPC IDs: the session rejects a duplicate in flight.
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/list"}`, 10+id)
		wg.Go(func() {
			assert.Equal(t, http.StatusOK, rawMCPStatus(t, a.url+"/mcp", body, withSession(nil, sessionID)))
			finished <- struct{}{}
		})
	}
	timeout := time.After(5 * time.Second)
wait:
	for range requests - 1 {
		select {
		case <-finished:
		case <-timeout:
			break wait
		}
	}
	close(store.gate)
	wg.Wait()
	assert.Equal(t, int64(1), store.gets.Load(), "close-mark reads")
	assert.Equal(t, int64(1), store.puts.Load(), "expiry extensions")
}

// slowServers is an upstream whose "slow" tool runs until release is called;
// defer it, as deferred calls run before cleanup closes the servers. It
// signals entered as each call starts.
func slowServers(t *testing.T) (servers map[string]ServerSpec, entered chan struct{}, release func()) {
	t.Helper()
	entered = make(chan struct{}, 1)
	released := make(chan struct{})
	release = sync.OnceFunc(func() { close(released) })
	upstream := newTestUpstream(t, "alpha", func(server *mcp.Server) {
		server.AddTool(&mcp.Tool{Name: "slow", InputSchema: map[string]any{"type": "object"}},
			func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				entered <- struct{}{}
				<-released
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, nil
			})
	})
	return map[string]ServerSpec{"alpha": testSpec("alpha", upstream, nil)}, entered, release
}

// startSlowCall starts a tools/call of the slow tool and returns its status.
func startSlowCall(t *testing.T, endpoint, sessionID string, entered chan struct{}) chan int {
	t.Helper()
	status := make(chan int, 1)
	body := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"` + NamespacedName("alpha", "slow") + `","arguments":{}}}`
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Session-Id", sessionID)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			status <- 0
			return
		}
		_ = resp.Body.Close()
		status <- resp.StatusCode
	}()
	<-entered
	return status
}

// A call that outlasts the idle timeout keeps its session's shared record:
// the replica running it extends the record while it runs, so the session
// still continues on another replica.
func TestLongCallKeepsTheSessionShared(t *testing.T) {
	clock := &testClock{}
	store := newMemorySharedStore(clock.Now)
	servers, entered, release := slowServers(t)
	defer release()
	a := newReplica(t, store, Options{ConfigServers: servers}, clock)
	a.service.postTouchInterval = 10 * time.Millisecond
	b := newReplica(t, store, Options{ConfigServers: servers}, clock)
	sessionID := initializeRawSession(t, a.url+"/mcp", nil)
	expiry := func() time.Time {
		entry, _ := store.entry(sharedSessionNamespace, sessionKey(sessionID))
		return entry.ExpiresAt
	}

	call := startSlowCall(t, a.url+"/mcp", sessionID, entered)
	clock.Advance(sessionIdleTimeout * 2 / 3)
	require.Eventually(t, func() bool { return expiry().After(clock.Now().Add(sessionIdleTimeout - time.Minute)) },
		5*time.Second, 5*time.Millisecond, "the call in flight extends the record")
	clock.Advance(sessionIdleTimeout * 2 / 3)
	assert.Equal(t, http.StatusOK, rawMCPStatus(t, b.url+"/mcp", listToolsBody, withSession(nil, sessionID)),
		"the session continues on another replica during the call")

	release()
	assert.Equal(t, http.StatusOK, <-call)
}

// The idle timeout restarts when a call ends, and so does the shared expiry:
// a session stays resumable for a full idle timeout after its last call.
func TestCallEndExtendsTheSharedSession(t *testing.T) {
	clock := &testClock{}
	store := newMemorySharedStore(clock.Now)
	servers, entered, release := slowServers(t)
	defer release()
	a := newReplica(t, store, Options{ConfigServers: servers}, clock)
	b := newReplica(t, store, Options{ConfigServers: servers}, clock)
	sessionID := initializeRawSession(t, a.url+"/mcp", nil)

	call := startSlowCall(t, a.url+"/mcp", sessionID, entered)
	clock.Advance(sessionIdleTimeout / 2)
	release()
	require.Equal(t, http.StatusOK, <-call)

	clock.Advance(sessionIdleTimeout * 3 / 4)
	assert.Equal(t, http.StatusOK, rawMCPStatus(t, b.url+"/mcp", listToolsBody, withSession(nil, sessionID)))
}

// The binding sweep keeps a session with a POST in flight, whose idle timeout
// is paused, and evicts it once it has been idle a timeout after the POST.
func TestSweepKeepsSessionsWithACallInFlight(t *testing.T) {
	clock := &testClock{}
	servers, entered, release := slowServers(t)
	defer release()
	a := newReplica(t, nil, Options{ConfigServers: servers}, clock)
	sessionID := initializeRawSession(t, a.url+"/mcp", nil)

	call := startSlowCall(t, a.url+"/mcp", sessionID, entered)
	clock.Advance(sessionIdleTimeout + 2*time.Minute)
	a.service.evictIdleBindings()
	_, bound := a.binding(sessionID)
	assert.True(t, bound, "a session with a call in flight is not idle")

	release()
	require.Equal(t, http.StatusOK, <-call)
	a.service.evictIdleBindings()
	_, bound = a.binding(sessionID)
	assert.True(t, bound, "the idle timeout counts from the end of the call")
	clock.Advance(sessionIdleTimeout + 2*time.Minute)
	a.service.evictIdleBindings()
	_, bound = a.binding(sessionID)
	assert.False(t, bound)
}

// failingListStore is a server store whose List fails while fail is set, so
// a Reload fails.
type failingListStore struct {
	*memoryStore
	fail atomic.Bool
}

func (s *failingListStore) List(ctx context.Context) ([]ManagedServer, error) {
	if s.fail.Load() {
		return nil, errors.New("store unavailable")
	}
	return s.memoryStore.List(ctx)
}

// A virtual server deleted from the store ends its sessions on the other
// replicas even when the deleting replica fails to reload afterwards.
func TestDeleteVirtualClosesSharedSessionsWhenReloadFails(t *testing.T) {
	store := newMemorySharedStore(time.Now)
	servers := alphaServers(t)
	virtualStore := &memoryVirtualStore{rows: map[string]ManagedVirtualServer{
		"coding": {Name: "coding", Servers: []string{"alpha"}},
	}}
	serverStore := &failingListStore{memoryStore: &memoryStore{rows: map[string]ManagedServer{}}}
	a := newReplica(t, store, Options{ConfigServers: servers, Store: serverStore, VirtualStore: virtualStore}, nil)
	b := newReplica(t, store, Options{ConfigServers: servers, Store: &memoryStore{rows: map[string]ManagedServer{}}, VirtualStore: virtualStore}, nil)
	sessionID := initializeRawSession(t, b.url+"/mcp/coding", nil)

	serverStore.fail.Store(true)
	require.Error(t, a.service.DeleteVirtual(context.Background(), "coding"))
	_, marked := store.entry(sharedSessionNamespace, endpointCloseKey(virtualBindingPrefix+"coding"))
	assert.True(t, marked, "the endpoint close mark is written despite the failed reload")
	b.clock.Advance(sessionRecheckInterval)
	assert.Equal(t, http.StatusNotFound, rawMCPStatus(t, b.url+"/mcp/coding", listToolsBody, withSession(nil, sessionID)))
}

// A virtual server recreated under the same name does not revive the deleted
// one's sessions, even when the replica that opened them runs ahead of the
// one that deleted it: its creation time, read from the store, tells the two
// apart without comparing replica clocks.
func TestRecreatedVirtualIgnoresReplicaClockSkew(t *testing.T) {
	store := newMemorySharedStore(time.Now)
	ctx := context.Background()
	servers := alphaServers(t)
	created := time.Now().Add(-time.Hour).UTC()
	virtualStore := &memoryVirtualStore{rows: map[string]ManagedVirtualServer{
		"coding": {Name: "coding", Servers: []string{"alpha"}, CreatedAt: created},
	}}
	options := func() Options {
		return Options{ConfigServers: servers, Store: &memoryStore{rows: map[string]ManagedServer{}}, VirtualStore: virtualStore}
	}
	ahead := &testClock{}
	ahead.Advance(time.Minute)
	a := newReplica(t, store, options(), ahead)
	b := newReplica(t, store, options(), nil)
	sessionID := initializeRawSession(t, a.url+"/mcp/coding", nil)

	require.NoError(t, b.service.DeleteVirtual(ctx, "coding"))
	_, err := b.service.UpsertVirtual(ctx, ManagedVirtualServer{Name: "coding", Servers: []string{"alpha"}, CreatedAt: created.Add(time.Second)})
	require.NoError(t, err)
	c := newReplica(t, store, options(), nil)
	assert.Equal(t, http.StatusNotFound, rawMCPStatus(t, c.url+"/mcp/coding", listToolsBody, withSession(nil, sessionID)),
		"no replica resumes the deleted virtual server's session")
	fresh := initializeRawSession(t, b.url+"/mcp/coding", nil)
	assert.Equal(t, http.StatusOK, rawMCPStatus(t, c.url+"/mcp/coding", listToolsBody, withSession(nil, fresh)),
		"a session on the recreated virtual server resumes")

	require.NoError(t, a.service.Reload(ctx))
	assert.Equal(t, http.StatusNotFound, rawMCPStatus(t, a.url+"/mcp/coding", listToolsBody, withSession(nil, sessionID)),
		"the replica hosting it ends it once it serves the recreated one")
	_, bound := a.binding(sessionID)
	assert.False(t, bound)
}

package mcpgateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/encryption/encryptiontest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// rotatingAuthServer is a token endpoint that rotates the refresh token on
// every refresh and, like reuse-detecting servers, counts every refresh
// token presented again after it was spent. With hold, every token request
// waits for the test to close release.
type rotatingAuthServer struct {
	*httptest.Server
	fail     bool
	release  chan struct{}
	released sync.Once

	mu        sync.Mutex
	current   string
	requests  int
	refreshes int
	reused    int
}

func newRotatingAuthServer(t *testing.T, hold bool) *rotatingAuthServer {
	t.Helper()
	as := &rotatingAuthServer{current: "refresh-1"}
	if hold {
		as.release = make(chan struct{})
	}
	as.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		as.mu.Lock()
		as.requests++
		as.mu.Unlock()
		if as.release != nil {
			<-as.release
		}
		as.mu.Lock()
		defer as.mu.Unlock()
		if as.fail || r.PostForm.Get("refresh_token") != as.current {
			if !as.fail {
				as.reused++
			}
			writeTestJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "refresh token revoked"})
			return
		}
		as.refreshes++
		as.current = fmt.Sprintf("refresh-%d", as.refreshes+1)
		writeTestJSON(w, http.StatusOK, map[string]any{
			"access_token": fmt.Sprintf("access-%d", as.refreshes+1), "refresh_token": as.current, "token_type": "Bearer", "expires_in": 3600,
		})
	}))
	t.Cleanup(as.Close)
	if hold {
		t.Cleanup(as.releaseHeld) // before Close, which waits for held handlers
	}
	return as
}

// releaseHeld lets held token requests finish.
func (as *rotatingAuthServer) releaseHeld() {
	as.released.Do(func() { close(as.release) })
}

func (as *rotatingAuthServer) requestCount() int {
	as.mu.Lock()
	defer as.mu.Unlock()
	return as.requests
}

// waitForRequest blocks until the first token request has arrived.
func (as *rotatingAuthServer) waitForRequest(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool { return as.requestCount() >= 1 }, time.Second, 10*time.Millisecond, "no token request arrived")
}

// assertNoSecondRequest checks that no further token request arrives while
// the first one is held, then lets the held requests finish.
func (as *rotatingAuthServer) assertNoSecondRequest(t *testing.T) {
	t.Helper()
	as.waitForRequest(t)
	assert.Never(t, func() bool { return as.requestCount() > 1 }, 300*time.Millisecond, 10*time.Millisecond, "a second token request was sent while the first was in flight")
	as.releaseHeld()
}

func (as *rotatingAuthServer) counts() (refreshes, reused int) {
	as.mu.Lock()
	defer as.mu.Unlock()
	return as.refreshes, as.reused
}

func rotatingRecord(as *rotatingAuthServer, expiry time.Time) oauthRecord {
	record := testOAuthRecord()
	record.TokenURL = as.URL + "/token"
	record.AccessToken = "access-1"
	record.RefreshToken = "refresh-1"
	record.Expiry = expiry
	return record
}

func newTestOAuthStore(t *testing.T) *oauthStore {
	t.Helper()
	store, err := NewSQLStore(context.Background(), sqlxtest.NewSQLite(t))
	require.NoError(t, err)
	box, _ := encryptiontest.NewBox(t)
	return newOAuthStore(store, box)
}

func TestOAuthViewDoesNotWaitForStalledRefresh(t *testing.T) {
	as := newRotatingAuthServer(t, true)
	record := rotatingRecord(as, time.Now().Add(-time.Minute))
	source := newOAuthSource("notion", record, nil, as.Client())
	go func() { _, _ = source.accessToken(context.Background()) }()
	as.waitForRequest(t)

	answered := make(chan struct{})
	go func() {
		source.view()
		_ = source.serverURL()
		_ = source.sameSignIn(record)
		source.adopt(record)
		close(answered)
	}()
	select {
	case <-answered:
	case <-time.After(time.Second):
		assert.Fail(t, "admin views waited behind a stalled token refresh")
	}
}

type tokenResult struct {
	token string
	err   error
}

// concurrentTokens asks n callers for an access token at once, spread over
// sources in turn, and returns a channel of their results.
func concurrentTokens(n int, sources ...*oauthSource) <-chan tokenResult {
	results := make(chan tokenResult, n)
	for i := range n {
		go func() {
			token, err := sources[i%len(sources)].accessToken(context.Background())
			results <- tokenResult{token, err}
		}()
	}
	return results
}

func assertAllTokens(t *testing.T, results <-chan tokenResult, want string) {
	t.Helper()
	for range cap(results) {
		got := <-results
		require.NoError(t, got.err)
		assert.Equal(t, want, got.token)
	}
}

func TestOAuthConcurrentCallersShareOneRefresh(t *testing.T) {
	as := newRotatingAuthServer(t, true)
	source := newOAuthSource("notion", rotatingRecord(as, time.Now().Add(-time.Minute)), nil, as.Client())
	tokens := concurrentTokens(8, source)
	as.assertNoSecondRequest(t)
	assertAllTokens(t, tokens, "access-2")
	refreshes, reused := as.counts()
	assert.Equal(t, 1, refreshes)
	assert.Zero(t, reused)
}

func TestOAuthReplicasSpendRefreshTokenOnce(t *testing.T) {
	as := newRotatingAuthServer(t, true)
	store := newTestOAuthStore(t)
	record := rotatingRecord(as, time.Now().Add(-time.Minute))
	require.NoError(t, store.put(context.Background(), "notion", record))
	// Two gateway instances share one store, each with its own copy.
	replicas := []*oauthSource{
		newOAuthSource("notion", record, store, as.Client()),
		newOAuthSource("notion", record, store, as.Client()),
	}

	tokens := concurrentTokens(10, replicas...)
	as.assertNoSecondRequest(t)
	assertAllTokens(t, tokens, "access-2")

	refreshes, reused := as.counts()
	assert.Equal(t, 1, refreshes, "one refresh serves both instances")
	assert.Zero(t, reused, "a spent refresh token is never sent again")
	stored, err := store.get(context.Background(), "notion")
	require.NoError(t, err)
	assert.Equal(t, "refresh-2", stored.RefreshToken)
	assert.Empty(t, stored.RefreshOwner, "the lease is cleared with the new tokens")
}

func TestOAuthRefreshWaitsForAnotherInstancesLease(t *testing.T) {
	as := newRotatingAuthServer(t, false)
	store := newTestOAuthStore(t)
	ctx := context.Background()
	record := rotatingRecord(as, time.Now().Add(-time.Minute))
	leased := record
	leased.RefreshOwner = "other-instance"
	leased.RefreshUntil = time.Now().Add(time.Minute)
	require.NoError(t, store.put(ctx, "notion", leased))

	source := newOAuthSource("notion", record, store, as.Client())
	results := concurrentTokens(1, source)
	assert.Never(t, func() bool { return len(results) > 0 }, 300*time.Millisecond, 10*time.Millisecond, "the refresh waits for the other instance's lease")

	// The other instance finishes its refresh.
	done := record
	done.AccessToken = "from-other-instance"
	done.RefreshToken = "refresh-elsewhere"
	done.Expiry = time.Now().Add(time.Hour)
	done.UpdatedAt = time.Now().UTC()
	require.NoError(t, store.put(ctx, "notion", done))
	assertAllTokens(t, results, "from-other-instance")
	refreshes, reused := as.counts()
	assert.Zero(t, refreshes)
	assert.Zero(t, reused)
}

func TestOAuthSaveAdoptsTokenStoredFirst(t *testing.T) {
	store := newTestOAuthStore(t)
	ctx := context.Background()
	base := testOAuthRecord()
	winner := base
	winner.AccessToken = "winner"
	winner.RefreshToken = "winner-refresh"
	winner.UpdatedAt = time.Now().UTC()
	require.NoError(t, store.put(ctx, "notion", winner))

	source := newOAuthSource("notion", base, store, nil)
	loser := base
	loser.AccessToken = "loser"
	loser.UpdatedAt = time.Now().UTC()
	assert.Equal(t, "winner", source.save(ctx, base, loser).AccessToken)
	stored, err := store.get(ctx, "notion")
	require.NoError(t, err)
	assert.Equal(t, "winner", stored.AccessToken, "the losing instance never overwrites the winner")
}

func TestBearerRoundTripperRefreshesOnceOnUnauthorized(t *testing.T) {
	tests := []struct {
		name          string
		accepts       string
		refreshFails  bool
		wantStatus    int
		wantUpstream  int
		wantRefreshes int
		wantState     string
	}{
		{name: "revoked token is refreshed and retried", accepts: "Bearer access-2", wantStatus: http.StatusOK, wantUpstream: 2, wantRefreshes: 1, wantState: OAuthStatusSignedIn},
		{name: "fresh token refused too is not retried again", accepts: "never", wantStatus: http.StatusUnauthorized, wantUpstream: 2, wantRefreshes: 1, wantState: OAuthStatusSignedIn},
		{name: "failed refresh returns the 401 and shows on the sign-in", accepts: "Bearer access-2", refreshFails: true, wantStatus: http.StatusUnauthorized, wantUpstream: 1, wantRefreshes: 0, wantState: OAuthStatusError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			as := newRotatingAuthServer(t, false)
			as.fail = tt.refreshFails
			var mu sync.Mutex
			var bodies []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				bodies = append(bodies, string(body))
				mu.Unlock()
				if r.Header.Get("Authorization") != tt.accepts {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(upstream.Close)

			// Unexpired, but revoked upstream.
			source := newOAuthSource("notion", rotatingRecord(as, time.Now().Add(time.Hour)), nil, as.Client())
			transport := &bearerRoundTripper{base: upstream.Client().Transport, source: source, origin: requestOrigin(upstream.URL)}
			req, err := http.NewRequest(http.MethodPost, upstream.URL+"/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0"}`))
			require.NoError(t, err)
			resp, err := transport.RoundTrip(req)
			require.NoError(t, err)
			_ = resp.Body.Close()

			assert.Equal(t, tt.wantStatus, resp.StatusCode)
			mu.Lock()
			assert.Len(t, bodies, tt.wantUpstream)
			for _, body := range bodies {
				assert.JSONEq(t, `{"jsonrpc":"2.0"}`, body, "the retry replays the body")
			}
			mu.Unlock()
			refreshes, _ := as.counts()
			assert.Equal(t, tt.wantRefreshes, refreshes)
			view := source.view()
			assert.Equal(t, tt.wantState, view.Status)
			if tt.refreshFails {
				assert.Contains(t, view.LastError, "invalid_grant")
			}
		})
	}
}

func TestBearerRoundTripperRejectedTokenWithoutRefreshToken(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(upstream.Close)
	record := testOAuthRecord()
	record.RefreshToken = ""
	source := newOAuthSource("notion", record, nil, nil)
	transport := &bearerRoundTripper{base: upstream.Client().Transport, source: source, origin: requestOrigin(upstream.URL)}
	req, err := http.NewRequest(http.MethodGet, upstream.URL, nil)
	require.NoError(t, err)
	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	view := source.view()
	assert.Equal(t, OAuthStatusError, view.Status)
	assert.Equal(t, errOAuthRejected.Error(), view.LastError)
}

// flakyOAuthRows wraps a row store so a test can fail its reads, or its
// writes from a given one on, the way an unreachable database would.
type flakyOAuthRows struct {
	oauthRowStore
	failReads      atomic.Bool
	writes         atomic.Int32
	failWritesFrom atomic.Int32 // fails every write from the n-th on; 0 fails none
}

func (f *flakyOAuthRows) getOAuth(ctx context.Context, name string) (string, error) {
	if f.failReads.Load() {
		return "", errors.New("store down")
	}
	return f.oauthRowStore.getOAuth(ctx, name)
}

func (f *flakyOAuthRows) swapOAuth(ctx context.Context, name, current, next string) (bool, error) {
	if n, from := f.writes.Add(1), f.failWritesFrom.Load(); from > 0 && n >= from {
		return false, errors.New("store down")
	}
	return f.oauthRowStore.swapOAuth(ctx, name, current, next)
}

func newFlakyOAuthStore(t *testing.T) (*oauthStore, *flakyOAuthRows) {
	t.Helper()
	store := newTestOAuthStore(t)
	flaky := &flakyOAuthRows{oauthRowStore: store.rows}
	store.rows = flaky
	return store, flaky
}

// A lease is written only over the exact record the instance read: when
// another instance refreshed meanwhile without rotating the refresh token,
// writing the lease would put the old access token back.
func TestOAuthLeaseRequiresUnchangedStoredRecord(t *testing.T) {
	store := newTestOAuthStore(t)
	ctx := context.Background()
	base := testOAuthRecord()
	require.NoError(t, store.put(ctx, "notion", base))
	newer := base
	newer.AccessToken = "newer"
	newer.Expiry = time.Now().Add(time.Hour)
	newer.UpdatedAt = time.Now().UTC()
	require.NoError(t, store.put(ctx, "notion", newer))

	source := newOAuthSource("notion", base, store, nil)
	stale := base
	leased, err := source.lease(ctx, stale)
	require.NoError(t, err)
	assert.False(t, leased)
	stored, err := store.get(ctx, "notion")
	require.NoError(t, err)
	assert.Equal(t, "newer", stored.AccessToken, "the newer token stays")
	assert.Empty(t, stored.RefreshOwner)
}

// A refresh whose save failed leaves the rotated refresh token in memory
// only. The next refresh must spend that one, not the spent one still in the
// store, and bring the store up to date.
func TestOAuthRenewKeepsNewerTokenWhenStoreIsBehind(t *testing.T) {
	as := newRotatingAuthServer(t, false)
	store, rows := newFlakyOAuthStore(t)
	ctx := context.Background()
	record := rotatingRecord(as, time.Now().Add(-time.Minute))
	require.NoError(t, store.put(ctx, "notion", record))
	source := newOAuthSource("notion", record, store, as.Client())

	// The lease write succeeds, the refresh succeeds, and the save fails.
	rows.failWritesFrom.Store(2)
	token, err := source.accessToken(ctx)
	require.NoError(t, err)
	assert.Equal(t, "access-2", token)
	stored, err := store.get(ctx, "notion")
	require.NoError(t, err)
	require.Equal(t, "refresh-1", stored.RefreshToken, "the store is behind")

	// The store recovers and the access token expires.
	rows.failWritesFrom.Store(0)
	source.mu.Lock()
	source.record.Expiry = time.Now().Add(-time.Minute)
	source.mu.Unlock()
	token, err = source.accessToken(ctx)
	require.NoError(t, err)
	assert.Equal(t, "access-3", token)
	refreshes, reused := as.counts()
	assert.Equal(t, 2, refreshes)
	assert.Zero(t, reused, "the spent refresh token is never sent again")
	stored, err = store.get(ctx, "notion")
	require.NoError(t, err)
	assert.Equal(t, "refresh-3", stored.RefreshToken)
	assert.Empty(t, stored.RefreshOwner)
}

// Without the store's coordination a refresh could spend a token another
// instance is spending too, so an unreadable store fails the request.
func TestOAuthRenewFailsClosedWhenStoreUnreadable(t *testing.T) {
	as := newRotatingAuthServer(t, false)
	store, rows := newFlakyOAuthStore(t)
	record := rotatingRecord(as, time.Now().Add(-time.Minute))
	require.NoError(t, store.put(context.Background(), "notion", record))
	rows.failReads.Store(true)
	source := newOAuthSource("notion", record, store, as.Client())

	_, err := source.accessToken(context.Background())
	require.ErrorContains(t, err, "read the stored OAuth tokens")
	refreshes, _ := as.counts()
	assert.Zero(t, refreshes, "no refresh token is spent without the store")
	assert.Equal(t, OAuthStatusError, source.view().Status)
}

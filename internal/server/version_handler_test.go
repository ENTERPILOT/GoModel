package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/versioncheck"
)

// versionTestServer wires a gateway whose update check reads manifests from a
// local test origin, and reports how many manifest requests it made.
func versionTestServer(t *testing.T, handler http.HandlerFunc) (*Server, *atomic.Int64, func() http.Header) {
	t.Helper()
	var calls atomic.Int64
	// The handler dispatches its check in the background, so the origin writes
	// these headers on another goroutine than the one asserting on them.
	var mu sync.Mutex
	var lastHeaders http.Header
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		lastHeaders = r.Header.Clone()
		mu.Unlock()
		calls.Add(1)
		handler(w, r)
	}))
	t.Cleanup(origin.Close)

	headers := func() http.Header {
		mu.Lock()
		defer mu.Unlock()
		return lastHeaders.Clone()
	}

	checker := versioncheck.New(versioncheck.Config{
		Enabled:   true,
		URL:       origin.URL + "/version",
		App:       "GoModel",
		Version:   "0.1.81",
		InstallID: "install-abc",
		Client:    origin.Client(),
	})
	return New(&mockProvider{}, &Config{VersionChecker: checker}), &calls, headers
}

func manifestOK(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("0.1.82\n"))
}

// awaitChecks waits for the background refresh the handler dispatches. The
// endpoint answers from cache and never blocks on the release host, so the
// manifest request lands after the response.
func awaitChecks(t *testing.T, calls *atomic.Int64, want int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() >= want {
			// Give a late extra call a chance to show up so "exactly want"
			// assertions are not merely racing ahead of it.
			time.Sleep(50 * time.Millisecond)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("made %d manifest requests, want %d", calls.Load(), want)
}

// visitCookie returns the value the response set for the visit cookie.
func visitCookie(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == versioncheck.CookieName {
			return cookie.Value
		}
	}
	return ""
}

func decodeStatus(t *testing.T, rec *httptest.ResponseRecorder) versioncheck.Status {
	t.Helper()
	var status versioncheck.Status
	err := json.Unmarshal(rec.Body.Bytes(), &status)
	require.NoError(t, err, "decode /version body %q: %v", rec.Body.String(), err)

	return status
}

func TestVersionEndpointChecksOnFirstVisit(t *testing.T) {
	srv, calls, headers := versionTestServer(t, manifestOK)

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64)")
	req.Host = "gateway.example.com"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	awaitChecks(t, calls, 1)
	value := headers().Get("X-GoModel-Host")
	assert.Empty(t, value)
	value = headers().Get("X-Forwarded-For")
	assert.Empty(t, value)
	assert.Equal(t, "Mozilla/5.0 (X11; Linux x86_64)", headers().Get("User-Agent"))

	date, id := versioncheck.SplitVisit(visitCookie(t, rec))
	require.Equal(t, time.Now().Format(time.DateOnly), date)
	require.NotEmpty(t, id)
	assert.Equal(t, headers().Get("X-GoModel-Date"), visitCookie(t, rec))
	cache := rec.Header().Get("Cache-Control")
	assert.Equal(t, "no-store", cache)
}

func TestVersionEndpointSkipsSecondVisitSameDay(t *testing.T) {
	srv, calls, _ := versionTestServer(t, manifestOK)

	first := httptest.NewRequest(http.MethodGet, "/version", nil)
	firstRec := httptest.NewRecorder()
	srv.ServeHTTP(firstRec, first)

	awaitChecks(t, calls, 1)

	second := httptest.NewRequest(http.MethodGet, "/version", nil)
	second.AddCookie(&http.Cookie{Name: versioncheck.CookieName, Value: visitCookie(t, firstRec)})
	secondRec := httptest.NewRecorder()
	srv.ServeHTTP(secondRec, second)

	require.Equal(t, int64(1), calls.Load())
	status := // The first visit's check has landed by now, so the cached answer carries it.
		decodeStatus(t, secondRec)
	require.Equal(t, "0.1.82", status.Latest, "cached response lost the result: %+v", status)
}

func TestVersionEndpointRechecksOnANewDay(t *testing.T) {
	srv, calls, _ := versionTestServer(t, manifestOK)

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	yesterday := time.Now().AddDate(0, 0, -1).Format(time.DateOnly)
	req.AddCookie(&http.Cookie{Name: versioncheck.CookieName, Value: yesterday + "-3f2504e0-4f89-11d3-9a0c-0305e82c3301"})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	awaitChecks(t, calls, 1)
	date, id := versioncheck.SplitVisit(visitCookie(t, rec))
	assert.Equal(t, time.Now().Format(time.DateOnly), date)
	assert.Equal(t, "3f2504e0-4f89-11d3-9a0c-0305e82c3301", id)
}

func TestVersionEndpointNeverForwardsCredentials(t *testing.T) {
	srv, calls, headers := versionTestServer(t, manifestOK)

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	req.Header.Set("Authorization", "Bearer sk-master-key")
	req.Header.Set("X-API-Key", "sk-provider-key")
	req.Header.Set("Referer", "https://gateway.example.com/admin/dashboard?token=leak")
	req.AddCookie(&http.Cookie{Name: "gomodel_session", Value: "super-secret"})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	awaitChecks(t, calls, 1)

	for _, forbidden := range []string{"Authorization", "X-API-Key", "Referer", "Cookie"} {
		value := headers().Get(forbidden)
		assert.Empty(t, value, "%s leaked to the release host as %q", forbidden, value)
	}
	for _, value := range headers() {
		assert.False(t, strings.Contains(strings.Join(value, " "), "sk-"), "a credential-shaped value reached the release host: %v", value)
	}
}

func TestVersionEndpointCookieIsReadableByTheDashboard(t *testing.T) {
	srv, _, _ := versionTestServer(t, manifestOK)

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == versioncheck.CookieName {
			cookie = c
		}
	}
	require.NotNil(t, cookie)
	assert.False(t, cookie.HttpOnly)
	assert.Equal(t, "/", cookie.Path)
	assert.Equal(t, versioncheck.CookieMaxAge, cookie.MaxAge)
	assert.Equal(t, http.SameSiteLaxMode, cookie.SameSite)

	// httptest requests are plain HTTP; Secure would make the browser drop
	// the cookie and turn the daily gate into a per-page-load check.
	assert.False(t, cookie.Secure)
}

func TestVersionEndpointSurvivesAnUnreachableManifest(t *testing.T) {
	srv, _, _ := versionTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	status := decodeStatus(t, rec)
	require.Equal(t, "0.1.81", status.Version)
	require.False(t, status.UpdateAvailable, "got %+v, want the local version with no update claimed", status)
}

func TestVersionEndpointWithoutACheckerReportsLocalBuild(t *testing.T) {
	srv := New(&mockProvider{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	status := decodeStatus(t, rec)
	require.NotEmpty(t, status.App)
	require.NotEmpty(t, status.Version, "got %+v, want the local build reported", status)
	assert.False(t, status.UpdateAvailable)
}

func TestVersionEndpointSkipsAuthentication(t *testing.T) {
	srv := New(&mockProvider{}, &Config{MasterKey: "sk-master-key"})

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}

// Greptile measured the first visit of the day taking the checker's whole
// timeout because the refresh ran inline. The endpoint must answer from cache
// regardless of how slow the release host is.
func TestVersionEndpointDoesNotWaitOnASlowManifest(t *testing.T) {
	release := make(chan struct{})
	srv, _, _ := versionTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		// Bounded: httptest.Server.Close waits for outstanding requests, so a
		// handler that could block forever would wedge the whole package if
		// the release below ever failed to reach it.
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
		_, _ = w.Write([]byte("0.1.82"))
	})
	// Registered after versionTestServer so it runs before that helper's
	// origin.Close cleanup: t.Cleanup is LIFO, and closing the origin first
	// would block on the stalled handler this test deliberately creates.
	t.Cleanup(func() { close(release) })

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	rec := httptest.NewRecorder()

	start := time.Now()
	srv.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	require.Equal(t, http.StatusOK, rec.Code)
	require.LessOrEqual(t, elapsed, time.Second)
	status := decodeStatus(t, rec)
	require.Equal(t, "0.1.81", status.Version, "got %+v, want the local build reported", status)
}

// /version is unauthenticated, and the visit id it accepts is echoed into an
// outbound request header and back into Set-Cookie. A caller-supplied id that
// is not the canonical UUID must be discarded rather than forwarded.
func TestVersionEndpointRejectsAForgedVisitID(t *testing.T) {
	srv, calls, headers := versionTestServer(t, manifestOK)

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	stale := time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	forged := stale + "-" + strings.Repeat("A", 120) + "-injected"
	req.AddCookie(&http.Cookie{Name: versioncheck.CookieName, Value: forged})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	awaitChecks(t, calls, 1)
	sent := headers().Get("X-GoModel-Date")
	assert.False(t, strings.Contains(sent, "injected"))
	assert.NotContains(t, sent, "AAAA", "X-GoModel-Date should discard the forged id")

	issued := visitCookie(t, rec)
	assert.False(t, strings.Contains(issued, "injected"))
	assert.NotContains(t, issued, "AAAA", "Set-Cookie should discard the forged id")

	date, id := versioncheck.SplitVisit(issued)
	require.Equal(t, time.Now().UTC().Format(time.DateOnly), date)
	require.NotEmpty(t, id)
}

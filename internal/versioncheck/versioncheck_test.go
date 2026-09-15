package versioncheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestChecker wires a Checker to a manifest server and a fixed clock.
func newTestChecker(t *testing.T, cfg Config, handler http.HandlerFunc) (*Checker, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg.Enabled = true
	cfg.URL = srv.URL + "/version"
	if cfg.App == "" {
		cfg.App = "GoModel"
	}
	if cfg.Version == "" {
		cfg.Version = "0.1.81"
	}
	cfg.Client = srv.Client()
	return New(cfg), srv
}

func TestManifestURL(t *testing.T) {
	tests := []struct {
		name string
		base string
		app  string
		want string
	}{
		{"core channel", "https://example.test/version/", "GoModel", "https://example.test/version/core.txt"},
		{"pro channel", "https://example.test/version/", "GoModel Pro", "https://example.test/version/pro.txt"},
		{"channel name is case insensitive", "https://example.test/version/", "gomodel pro", "https://example.test/version/pro.txt"},
		{"custom distribution reads core", "https://example.test/version/", "Custom Gateway", "https://example.test/version/core.txt"},
		{"no trailing slash", "https://example.test/version", "GoModel", "https://example.test/version/core.txt"},
		// A mirror that authenticates with a query parameter must still be
		// asked for the manifest, not for the base path with the file glued
		// onto the end of the query.
		{"query-authenticated mirror", "https://mirror.test/version?token=abc", "GoModel", "https://mirror.test/version/core.txt?token=abc"},
		{"port and subpath", "http://mirror.test:8080/a/b", "GoModel Pro", "http://mirror.test:8080/a/b/pro.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := manifestURL(tt.base, tt.app)
			require.Equal(t, tt.want, got, "manifestURL(%q, %q) = %q, want %q", tt.base, tt.app, got, tt.want)

		})
	}
}

// A configured mirror can carry a secret in userinfo, the query, or the path.
// None of them may reach an error message or a log line.
func TestSafeURLKeepsOnlySchemeAndHost(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"https://user:hunter2@mirror.test/version/core.txt", "https://mirror.test"},
		{"https://mirror.test/version/core.txt?token=abc", "https://mirror.test"},
		{"https://mirror.test/s3cr3t-path/core.txt", "https://mirror.test"},
		{"http://mirror.test:8080/core.txt#frag", "http://mirror.test:8080"},
		{"://not a url", "the configured manifest host"},
		{"core.txt", "the configured manifest host"},
	}
	for _, tt := range tests {
		t.Run(tt.want+" "+tt.raw, func(t *testing.T) {
			got := SafeURL(tt.raw)
			require.Equal(t, tt.want, got, "SafeURL(%q) = %q, want %q", tt.raw, got, tt.want)

			for _, secret := range []string{"hunter2", "token=abc", "s3cr3t"} {
				require.NotContains(t, got, secret, "SafeURL(%q) leaked %q", tt.raw, secret)
			}
		})
	}
}

func TestRefreshReportsUpdate(t *testing.T) {
	checker, _ := newTestChecker(t, Config{Version: "0.1.81"}, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("0.1.82\n"))
	})

	status, err := checker.Refresh(context.Background(), Beacon{})
	require.NoError(t, err)
	require.Equal(t, "0.1.82", status.Latest)
	require.True(t, status.UpdateAvailable, "got %+v, want latest 0.1.82 with an update", status)
	require.NotEmpty(t, status.CheckedAt)
}

func TestRefreshSendsIdentityHeaders(t *testing.T) {
	var got http.Header
	checker, _ := newTestChecker(t, Config{
		App:       "GoModel Pro",
		Version:   "1.0.0-pro",
		InstallID: "install-123",
	}, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte("1.0.0-pro"))
	})
	_, err := checker.Refresh(context.Background(), Beacon{})
	require.NoError(t, err)

	for header, want := range map[string]string{
		"X-Gomodel-Version": "1.0.0-pro",
		"X-Gomodel-App":     "GoModel Pro",
		"X-Gomodel-Install": "install-123",
		"X-Gomodel-Source":  "scheduled",
	} {
		assert.Equal(t, want, got.Get(header))
	}
}

func TestRefreshAsksInstallIDFuncPerRequest(t *testing.T) {
	var sent []string
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	current := "provisional-id"
	checker, _ := newTestChecker(t, Config{
		InstallID:     "fixed-id",
		InstallIDFunc: func(context.Context) string { return current },
		Now:           func() time.Time { return now },
	}, func(w http.ResponseWriter, r *http.Request) {
		sent = append(sent, r.Header.Get("X-GoModel-Install"))
		_, _ = w.Write([]byte("0.1.82"))
	})
	_, err := // The database answered between the two checks and the id it holds
		// differs from the provisional one: the second request must carry it.
		checker.Refresh(context.Background(), Beacon{})
	require.NoError(t, err)

	current = "database-id"
	now = now.Add(2 * minRefreshInterval)
	_, err = checker.Refresh(context.Background(), Beacon{})
	require.NoError(t, err)
	want := []string{"provisional-id", "database-id"}
	require.True(t, slices.Equal(sent, want), "X-GoModel-Install per request = %q, want %q (InstallID must not override the func)", sent, want)
}

func TestRefreshForwardsOnlyAllowlistedBrowserHeaders(t *testing.T) {
	var got http.Header
	checker, _ := newTestChecker(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte("0.1.82"))
	})

	inbound := httptest.NewRequest(http.MethodGet, "https://gateway.example.com/version", nil)
	inbound.Host = "gateway.example.com"
	inbound.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh)")
	inbound.Header.Set("Accept-Language", "en-GB,en;q=0.9")
	inbound.Header.Set("Sec-CH-UA-Platform", `"macOS"`)
	inbound.Header.Set("Cookie", "session=super-secret")
	inbound.Header.Set("Authorization", "Bearer sk-master-key")
	inbound.Header.Set("X-API-Key", "sk-provider-key")
	inbound.Header.Set("Referer", "https://gateway.example.com/admin/dashboard?key=leak")

	beacon := BeaconFromRequest(inbound, "2026-08-26-abc")
	_, err := checker.Refresh(context.Background(), beacon)
	require.NoError(t, err)
	assert.Equal(t, "Mozilla/5.0 (Macintosh)", got.Get("User-Agent"))
	assert.Equal(t, "en-GB,en;q=0.9", got.Get("Accept-Language"))
	assert.Equal(t, `"macOS"`, got.Get("Sec-CH-UA-Platform"))
	value := // The dashboard's hostname identifies the operator's organization, so it
		// is never forwarded.
		got.Get("X-GoModel-Host")
	assert.Empty(t, value)
	assert.Equal(t, "2026-08-26-abc", got.Get("X-GoModel-Date"))
	value = // Client addresses are personal data and are never forwarded.
		got.Get("X-Forwarded-For")
	assert.Empty(t, value)
	assert.Equal(t, "dashboard", got.Get("X-GoModel-Source"))

	for _, forbidden := range []string{"Cookie", "Authorization", "X-API-Key", "Referer"} {
		value := got.Get(forbidden)
		assert.Empty(t, value, "%s leaked upstream as %q", forbidden, value)
	}
}

func TestRefreshThrottlesRepeatedCalls(t *testing.T) {
	var calls atomic.Int64
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	checker, _ := newTestChecker(t, Config{Now: func() time.Time { return now }}, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte("0.1.82"))
	})

	// Scheduled checks only; a burst of them must collapse to one.
	for range 5 {
		_, err := checker.Refresh(context.Background(), Beacon{})
		require.NoError(t, err)
	}
	require.Equal(t, int64(1), calls.Load())
}

func TestRefreshRespectsDailyBudget(t *testing.T) {
	var calls atomic.Int64
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	checker, _ := newTestChecker(t, Config{
		MaxDailyChecks: 2,
		Now:            func() time.Time { return now },
	}, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte("0.1.82"))
	})

	// Step past the per-request throttle between attempts so only the daily
	// budget can stop them.
	for range 4 {
		_, err := checker.Refresh(context.Background(), Beacon{})
		require.NoError(t, err)

		now = now.Add(2 * minRefreshInterval)
	}
	require.Equal(t, int64(2), calls.Load())
}

func TestRefreshKeepsCachedStatusOnFailure(t *testing.T) {
	fail := false
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	checker, _ := newTestChecker(t, Config{Now: func() time.Time { return now }}, func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("0.1.82"))
	})
	_, err := checker.Refresh(context.Background(), Beacon{})
	require.NoError(t, err)

	fail = true
	now = now.Add(2 * minRefreshInterval)

	status, err := checker.Refresh(context.Background(), Beacon{})
	require.Error(t, err)
	require.Equal(t, "0.1.82", status.Latest)
	require.True(t, status.UpdateAvailable, "got %+v, want the previously cached result", status)
}

func TestRefreshRejectsNonVersionBody(t *testing.T) {
	checker, _ := newTestChecker(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<!doctype html><html>404</html>"))
	})

	status, err := checker.Refresh(context.Background(), Beacon{})
	require.Error(t, err)
	require.Empty(t, status.Latest)
}

func TestRefreshRejectsOversizedManifest(t *testing.T) {
	checker, _ := newTestChecker(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("9", 4096)))
	})

	// Truncating would leave a plausible-looking but wrong version in the
	// cache, so an oversized body must be refused outright.
	status, err := checker.Refresh(context.Background(), Beacon{})
	require.Error(t, err)
	require.Empty(t, status.Latest)
}

func TestDisabledCheckerNeverCallsOut(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte("0.1.82"))
	}))
	defer srv.Close()

	checker := New(Config{Enabled: false, URL: srv.URL, App: "GoModel", Version: "0.1.81", Client: srv.Client()})
	_, err := checker.Refresh(context.Background(), Beacon{})
	require.NoError(t, err)

	checker.Run(context.Background())

	require.Equal(t, int64(0), calls.Load())

	status := checker.Status()
	require.False(t, status.Enabled)
	require.Equal(t, "0.1.81", status.Version, "got %+v, want the local version with Enabled false", status)
}

func TestLeaksQueryInCleartext(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"http with a query token", "http://mirror.test/version?token=abc", true},
		{"https with a query token", "https://mirror.test/version?token=abc", false},
		{"http without a query", "http://mirror.test/version", false},
		{"https without a query", "https://mirror.test/version", false},
		{"default url", DefaultURL, false},
		{"unparseable", "://nope", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := LeaksQueryInCleartext(tt.raw)
			require.Equal(t, tt.want, got, "LeaksQueryInCleartext(%q) = %v, want %v", tt.raw, got, tt.want)

		})
	}
}

package versioncheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

func TestManifestURLPicksChannel(t *testing.T) {
	tests := []struct {
		app  string
		want string
	}{
		{"GoModel", "https://example.test/version/core.txt"},
		{"GoModel Pro", "https://example.test/version/pro.txt"},
		{"gomodel pro", "https://example.test/version/pro.txt"},
		{"Custom Gateway", "https://example.test/version/core.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.app, func(t *testing.T) {
			if got := manifestURL("https://example.test/version/", tt.app); got != tt.want {
				t.Fatalf("manifestURL(%q) = %q, want %q", tt.app, got, tt.want)
			}
		})
	}
}

func TestRefreshReportsUpdate(t *testing.T) {
	checker, _ := newTestChecker(t, Config{Version: "0.1.81"}, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("0.1.82\n"))
	})

	status, err := checker.Refresh(context.Background(), Beacon{})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if status.Latest != "0.1.82" || !status.UpdateAvailable {
		t.Fatalf("got %+v, want latest 0.1.82 with an update", status)
	}
	if status.CheckedAt == "" {
		t.Fatal("expected CheckedAt to be stamped")
	}
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

	if _, err := checker.Refresh(context.Background(), Beacon{}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	for header, want := range map[string]string{
		"X-Gomodel-Version": "1.0.0-pro",
		"X-Gomodel-App":     "GoModel Pro",
		"X-Gomodel-Install": "install-123",
		"X-Gomodel-Source":  "scheduled",
	} {
		if got.Get(header) != want {
			t.Errorf("%s = %q, want %q", header, got.Get(header), want)
		}
	}
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
	if _, err := checker.Refresh(context.Background(), beacon); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if got.Get("User-Agent") != "Mozilla/5.0 (Macintosh)" {
		t.Errorf("User-Agent = %q, want the browser's", got.Get("User-Agent"))
	}
	if got.Get("Accept-Language") != "en-GB,en;q=0.9" {
		t.Errorf("Accept-Language = %q", got.Get("Accept-Language"))
	}
	if got.Get("Sec-CH-UA-Platform") != `"macOS"` {
		t.Errorf("Sec-CH-UA-Platform = %q", got.Get("Sec-CH-UA-Platform"))
	}
	// The dashboard's hostname identifies the operator's organization, so it
	// is never forwarded.
	if value := got.Get("X-GoModel-Host"); value != "" {
		t.Errorf("X-GoModel-Host = %q, want the hostname kept local", value)
	}
	if got.Get("X-GoModel-Date") != "2026-08-26-abc" {
		t.Errorf("X-GoModel-Date = %q", got.Get("X-GoModel-Date"))
	}
	// Client addresses are personal data and are never forwarded.
	if value := got.Get("X-Forwarded-For"); value != "" {
		t.Errorf("X-Forwarded-For = %q, want the address kept local", value)
	}
	if got.Get("X-GoModel-Source") != "dashboard" {
		t.Errorf("X-GoModel-Source = %q, want dashboard", got.Get("X-GoModel-Source"))
	}
	for _, forbidden := range []string{"Cookie", "Authorization", "X-API-Key", "Referer"} {
		if value := got.Get(forbidden); value != "" {
			t.Errorf("%s leaked upstream as %q", forbidden, value)
		}
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
		if _, err := checker.Refresh(context.Background(), Beacon{}); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("made %d manifest requests, want 1 within the throttle window", calls.Load())
	}
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
		if _, err := checker.Refresh(context.Background(), Beacon{}); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		now = now.Add(2 * minRefreshInterval)
	}
	if calls.Load() != 2 {
		t.Fatalf("made %d manifest requests, want the 2 the budget allows", calls.Load())
	}
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

	if _, err := checker.Refresh(context.Background(), Beacon{}); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}
	fail = true
	now = now.Add(2 * minRefreshInterval)

	status, err := checker.Refresh(context.Background(), Beacon{})
	if err == nil {
		t.Fatal("expected the failed manifest fetch to report an error")
	}
	if status.Latest != "0.1.82" || !status.UpdateAvailable {
		t.Fatalf("got %+v, want the previously cached result", status)
	}
}

func TestRefreshRejectsNonVersionBody(t *testing.T) {
	checker, _ := newTestChecker(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<!doctype html><html>404</html>"))
	})

	status, err := checker.Refresh(context.Background(), Beacon{})
	if err == nil {
		t.Fatal("expected an error for an HTML body")
	}
	if status.Latest != "" {
		t.Fatalf("latest = %q, want it left unset", status.Latest)
	}
}

func TestRefreshRejectsOversizedManifest(t *testing.T) {
	checker, _ := newTestChecker(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("9", 4096)))
	})

	// Truncating would leave a plausible-looking but wrong version in the
	// cache, so an oversized body must be refused outright.
	status, err := checker.Refresh(context.Background(), Beacon{})
	if err == nil {
		t.Fatal("expected an error for an oversized manifest")
	}
	if status.Latest != "" {
		t.Fatalf("latest = %q, want the cache left untouched", status.Latest)
	}
}

func TestDisabledCheckerNeverCallsOut(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte("0.1.82"))
	}))
	defer srv.Close()

	checker := New(Config{Enabled: false, URL: srv.URL, App: "GoModel", Version: "0.1.81", Client: srv.Client()})
	if _, err := checker.Refresh(context.Background(), Beacon{}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	checker.Run(context.Background())

	if calls.Load() != 0 {
		t.Fatalf("a disabled checker made %d requests", calls.Load())
	}
	status := checker.Status()
	if status.Enabled || status.Version != "0.1.81" {
		t.Fatalf("got %+v, want the local version with Enabled false", status)
	}
}
